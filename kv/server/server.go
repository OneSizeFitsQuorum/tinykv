package server

import (
	"context"
	"github.com/pingcap-incubator/tinykv/kv/coprocessor"
	"github.com/pingcap-incubator/tinykv/kv/raftstore/util"
	"github.com/pingcap-incubator/tinykv/kv/storage"
	"github.com/pingcap-incubator/tinykv/kv/storage/raft_storage"
	"github.com/pingcap-incubator/tinykv/kv/transaction/latches"
	"github.com/pingcap-incubator/tinykv/kv/transaction/mvcc"
	coppb "github.com/pingcap-incubator/tinykv/proto/pkg/coprocessor"
	"github.com/pingcap-incubator/tinykv/proto/pkg/kvrpcpb"
	"github.com/pingcap-incubator/tinykv/proto/pkg/tinykvpb"
	"github.com/pingcap/tidb/kv"
)

var _ tinykvpb.TinyKvServer = new(Server)

// Server is a TinyKV server, it 'faces outwards', sending and receiving messages from clients such as TinySQL.
type Server struct {
	storage storage.Storage

	// (Used in 4A/4B)
	Latches *latches.Latches

	// coprocessor API handler, out of course scope
	copHandler *coprocessor.CopHandler
}

func NewServer(storage storage.Storage) *Server {
	return &Server{
		storage: storage,
		Latches: latches.NewLatches(),
	}
}

// The below functions are Server's gRPC API (implements TinyKvServer).

// Raft commands (tinykv <-> tinykv)
// Only used for RaftStorage, so trivially forward it.
func (server *Server) Raft(stream tinykvpb.TinyKv_RaftServer) error {
	return server.storage.(*raft_storage.RaftStorage).Raft(stream)
}

// Snapshot stream (tinykv <-> tinykv)
// Only used for RaftStorage, so trivially forward it.
func (server *Server) Snapshot(stream tinykvpb.TinyKv_SnapshotServer) error {
	return server.storage.(*raft_storage.RaftStorage).Snapshot(stream)
}

// Transactional API.
func (server *Server) KvGet(_ context.Context, req *kvrpcpb.GetRequest) (*kvrpcpb.GetResponse, error) {
	reader, err := server.storage.Reader(req.Context)
	if err != nil {
		return &kvrpcpb.GetResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
	}
	defer reader.Close()
	mvccTxn := mvcc.NewMvccTxn(reader, req.Version)
	lock, err := mvccTxn.GetLock(req.Key)
	if err != nil {
		return &kvrpcpb.GetResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
	}
	response := new(kvrpcpb.GetResponse)
	if lock.IsLockedFor(req.Key, mvccTxn.StartTS, response) {
		return response, nil
	}
	value, err := mvccTxn.GetValue(req.Key)
	if err != nil {
		return &kvrpcpb.GetResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
	}
	server.Latches.Validation(mvccTxn, [][]byte{req.Key})
	return &kvrpcpb.GetResponse{
		Value:    value,
		NotFound: value == nil,
	}, nil
}

func (server *Server) KvPrewrite(_ context.Context, req *kvrpcpb.PrewriteRequest) (*kvrpcpb.PrewriteResponse, error) {
	if len(req.Mutations) == 0 {
		return &kvrpcpb.PrewriteResponse{}, nil
	}
	reader, err := server.storage.Reader(req.Context)
	if err != nil {
		return &kvrpcpb.PrewriteResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
	}
	defer reader.Close()
	mvccTxn := mvcc.NewMvccTxn(reader, req.StartVersion)
	for _, mutation := range req.Mutations {
		write, version, err := mvccTxn.MostRecentWrite(mutation.Key)
		if err != nil {
			return &kvrpcpb.PrewriteResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
		}
		if version > mvccTxn.StartTS {
			return &kvrpcpb.PrewriteResponse{Errors: []*kvrpcpb.KeyError{{Conflict: &kvrpcpb.WriteConflict{
				StartTs:    req.StartVersion,
				ConflictTs: write.StartTS,
				Key:        mutation.Key,
				Primary:    req.PrimaryLock,
			}}}}, nil
		}
		lock, err := mvccTxn.GetLock(mutation.Key)
		if err != nil {
			return &kvrpcpb.PrewriteResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
		}
		if lock != nil {
			return &kvrpcpb.PrewriteResponse{Errors: []*kvrpcpb.KeyError{{Conflict: &kvrpcpb.WriteConflict{
				StartTs:    req.StartVersion,
				ConflictTs: lock.Ts,
				Key:        mutation.Key,
				Primary:    req.PrimaryLock,
			}}}}, nil
		}
		mvccTxn.PutValue(mutation.Key, mutation.Value)
		mvccTxn.PutLock(mutation.Key, &mvcc.Lock{Primary: req.PrimaryLock, Ts: req.StartVersion, Ttl: req.LockTtl, Kind: mvcc.WriteKindFromProto(mutation.Op)})
	}
	err = server.storage.Write(req.Context, mvccTxn.Writes())
	if err != nil {
		return &kvrpcpb.PrewriteResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
	}
	return &kvrpcpb.PrewriteResponse{}, nil
}

func (server *Server) KvCommit(_ context.Context, req *kvrpcpb.CommitRequest) (*kvrpcpb.CommitResponse, error) {
	if len(req.Keys) == 0 {
		return &kvrpcpb.CommitResponse{}, nil
	}
	reader, err := server.storage.Reader(req.Context)
	if err != nil {
		return &kvrpcpb.CommitResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
	}
	defer reader.Close()
	mvccTxn := mvcc.NewMvccTxn(reader, req.StartVersion)
	server.Latches.WaitForLatches(req.Keys)
	defer server.Latches.ReleaseLatches(req.Keys)
	for _, key := range req.Keys {
		lock, err := mvccTxn.GetLock(key)
		if err != nil {
			return &kvrpcpb.CommitResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
		}
		if lock == nil {
			return &kvrpcpb.CommitResponse{}, nil
		}
		if req.StartVersion != lock.Ts {
			return &kvrpcpb.CommitResponse{Error: &kvrpcpb.KeyError{Retryable: string(lock.Primary)}}, nil
		}
		if !lock.IsLockedFor(key, mvccTxn.StartTS, new(kvrpcpb.CommitResponse)) {
			return &kvrpcpb.CommitResponse{Error: &kvrpcpb.KeyError{Locked: lock.Info(key)}}, nil
		}
		mvccTxn.PutWrite(key, req.CommitVersion, &mvcc.Write{StartTS: req.StartVersion, Kind: lock.Kind})
		mvccTxn.DeleteLock(key)
	}
	err = server.storage.Write(req.Context, mvccTxn.Writes())
	if err != nil {
		return &kvrpcpb.CommitResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
	}
	return &kvrpcpb.CommitResponse{}, nil
}

func (server *Server) KvScan(_ context.Context, req *kvrpcpb.ScanRequest) (*kvrpcpb.ScanResponse, error) {
	reader, err := server.storage.Reader(req.Context)
	if err != nil {
		return &kvrpcpb.ScanResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
	}
	defer reader.Close()
	scanner := mvcc.NewScanner(req.StartKey, mvcc.NewMvccTxn(reader, req.Version))
	defer scanner.Close()
	response := new(kvrpcpb.ScanResponse)
	for limit := req.Limit; limit > 0; limit-- {
		key, value, err := scanner.Next()
		if err != nil {
			if keyError, ok := err.(*mvcc.KeyError); ok {
				response.Pairs = append(response.Pairs, &kvrpcpb.KvPair{Error: &kvrpcpb.KeyError{Locked: keyError.Locked}})
				continue
			} else {
				return &kvrpcpb.ScanResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
			}
		}
		if key == nil {
			return response, nil
		}
		response.Pairs = append(response.Pairs, &kvrpcpb.KvPair{Key: key, Value: value})
	}

	return response, nil
}

func (server *Server) KvCheckTxnStatus(_ context.Context, req *kvrpcpb.CheckTxnStatusRequest) (*kvrpcpb.CheckTxnStatusResponse, error) {
	reader, err := server.storage.Reader(req.Context)
	if err != nil {
		return &kvrpcpb.CheckTxnStatusResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
	}
	defer reader.Close()
	mvccTxn := mvcc.NewMvccTxn(reader, req.LockTs)
	server.Latches.WaitForLatches([][]byte{req.PrimaryKey})
	defer server.Latches.ReleaseLatches([][]byte{req.PrimaryKey})
	lock, err := mvccTxn.GetLock(req.PrimaryKey)
	if err != nil {
		return &kvrpcpb.CheckTxnStatusResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
	}
	if lock != nil && lock.Ts == req.LockTs {
		if mvcc.PhysicalTime(lock.Ts)+lock.Ttl < mvcc.PhysicalTime(req.CurrentTs) {
			mvccTxn.DeleteValue(req.PrimaryKey)
			mvccTxn.DeleteLock(req.PrimaryKey)
			mvccTxn.PutWrite(req.PrimaryKey, mvccTxn.StartTS, &mvcc.Write{StartTS: mvccTxn.StartTS, Kind: mvcc.WriteKindRollback})
			err = server.storage.Write(req.Context, mvccTxn.Writes())
			if err != nil {
				return &kvrpcpb.CheckTxnStatusResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
			}
			return &kvrpcpb.CheckTxnStatusResponse{Action: kvrpcpb.Action_TTLExpireRollback}, nil
		} else {
			return &kvrpcpb.CheckTxnStatusResponse{Action: kvrpcpb.Action_NoAction}, nil
		}
	}
	existingWrite, commitTs, err := mvccTxn.CurrentWrite(req.PrimaryKey)
	if err != nil {
		return nil, err
	}
	if existingWrite == nil {
		mvccTxn.PutWrite(req.PrimaryKey, mvccTxn.StartTS, &mvcc.Write{StartTS: mvccTxn.StartTS, Kind: mvcc.WriteKindRollback})
		err = server.storage.Write(req.Context, mvccTxn.Writes())
		if err != nil {
			return &kvrpcpb.CheckTxnStatusResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
		}
		return &kvrpcpb.CheckTxnStatusResponse{Action: kvrpcpb.Action_LockNotExistRollback}, nil
	}

	if existingWrite.Kind == mvcc.WriteKindRollback {
		return &kvrpcpb.CheckTxnStatusResponse{Action: kvrpcpb.Action_NoAction}, nil
	}

	err = server.storage.Write(req.Context, mvccTxn.Writes())
	if err != nil {
		return &kvrpcpb.CheckTxnStatusResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
	}
	return &kvrpcpb.CheckTxnStatusResponse{Action: kvrpcpb.Action_NoAction, CommitVersion: commitTs}, nil
}

func (server *Server) KvBatchRollback(_ context.Context, req *kvrpcpb.BatchRollbackRequest) (*kvrpcpb.BatchRollbackResponse, error) {
	if len(req.Keys) == 0 {
		return &kvrpcpb.BatchRollbackResponse{}, nil
	}
	reader, err := server.storage.Reader(req.Context)
	if err != nil {
		return &kvrpcpb.BatchRollbackResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
	}
	defer reader.Close()
	mvccTxn := mvcc.NewMvccTxn(reader, req.StartVersion)
	server.Latches.WaitForLatches(req.Keys)
	defer server.Latches.ReleaseLatches(req.Keys)
	for _, key := range req.Keys {
		lock, err := mvccTxn.GetLock(key)
		if err != nil {
			return &kvrpcpb.BatchRollbackResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
		}
		if lock == nil || lock.Ts != mvccTxn.StartTS {
			existingWrite, _, err := mvccTxn.CurrentWrite(key)
			if err != nil {
				return &kvrpcpb.BatchRollbackResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
			}
			if existingWrite != nil {
				if existingWrite.Kind == mvcc.WriteKindRollback {
					return &kvrpcpb.BatchRollbackResponse{}, nil
				}
				return &kvrpcpb.BatchRollbackResponse{Error: &kvrpcpb.KeyError{Abort: "true"}}, nil
			} else {
				mvccTxn.PutWrite(key, mvccTxn.StartTS, &mvcc.Write{StartTS: mvccTxn.StartTS, Kind: mvcc.WriteKindRollback})
				err = server.storage.Write(req.Context, mvccTxn.Writes())
				if err != nil {
					return &kvrpcpb.BatchRollbackResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
				}
				return &kvrpcpb.BatchRollbackResponse{}, nil
			}
		}
		mvccTxn.DeleteValue(key)
		mvccTxn.PutWrite(key, mvccTxn.StartTS, &mvcc.Write{StartTS: mvccTxn.StartTS, Kind: mvcc.WriteKindRollback})
		mvccTxn.DeleteLock(key)
	}
	err = server.storage.Write(req.Context, mvccTxn.Writes())
	if err != nil {
		return &kvrpcpb.BatchRollbackResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
	}
	return &kvrpcpb.BatchRollbackResponse{}, nil
}

func (server *Server) KvResolveLock(_ context.Context, req *kvrpcpb.ResolveLockRequest) (*kvrpcpb.ResolveLockResponse, error) {
	reader, err := server.storage.Reader(req.Context)
	if err != nil {
		return &kvrpcpb.ResolveLockResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
	}
	defer reader.Close()
	mvccTxn := mvcc.NewMvccTxn(reader, req.StartVersion)

	klPair, err := mvcc.AllLocksForTxn(mvccTxn)

	for _, pair := range klPair {
		if req.CommitVersion == 0 {
			mvccTxn.DeleteValue(pair.Key)
			mvccTxn.PutWrite(pair.Key, mvccTxn.StartTS, &mvcc.Write{StartTS: mvccTxn.StartTS, Kind: mvcc.WriteKindRollback})
			mvccTxn.DeleteLock(pair.Key)
		} else {
			mvccTxn.PutWrite(pair.Key, req.CommitVersion, &mvcc.Write{StartTS: req.StartVersion, Kind: pair.Lock.Kind})
			mvccTxn.DeleteLock(pair.Key)
		}
	}
	err = server.storage.Write(req.Context, mvccTxn.Writes())
	if err != nil {
		return &kvrpcpb.ResolveLockResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
	}
	return &kvrpcpb.ResolveLockResponse{}, nil
}

// SQL push down commands.
func (server *Server) Coprocessor(_ context.Context, req *coppb.Request) (*coppb.Response, error) {
	resp := new(coppb.Response)
	reader, err := server.storage.Reader(req.Context)
	if err != nil {
		if regionErr, ok := err.(*raft_storage.RegionError); ok {
			resp.RegionError = regionErr.RequestErr
			return resp, nil
		}
		return nil, err
	}
	switch req.Tp {
	case kv.ReqTypeDAG:
		return server.copHandler.HandleCopDAGRequest(reader, req), nil
	case kv.ReqTypeAnalyze:
		return server.copHandler.HandleCopAnalyzeRequest(reader, req), nil
	}
	return nil, nil
}
