package server

import (
	"context"
	"github.com/pingcap-incubator/tinykv/kv/coprocessor"
	"github.com/pingcap-incubator/tinykv/kv/storage"
	"github.com/pingcap-incubator/tinykv/kv/storage/raft_storage"
	"github.com/pingcap-incubator/tinykv/kv/transaction/command"
	"github.com/pingcap-incubator/tinykv/kv/transaction/latches"
	coppb "github.com/pingcap-incubator/tinykv/proto/pkg/coprocessor"
	"github.com/pingcap-incubator/tinykv/proto/pkg/kvrpcpb"
	"github.com/pingcap-incubator/tinykv/proto/pkg/tinykvpb"
	"github.com/pingcap/tidb/kv"
	"reflect"
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
	response, err := command.ExecuteQuery(command.NewGetCommand(req), server.storage)
	if err != nil {
		if response, err = regionError(new(kvrpcpb.GetResponse), err); err != nil {
			return nil, err
		}
	}
	return response.(*kvrpcpb.GetResponse), err
}

func (server *Server) KvScan(_ context.Context, req *kvrpcpb.ScanRequest) (*kvrpcpb.ScanResponse, error) {
	response, err := command.ExecuteQuery(command.NewScanCommand(req), server.storage)
	if err != nil {
		if response, err = regionError(new(kvrpcpb.ScanResponse), err); err != nil {
			return nil, err
		}
	}
	return response.(*kvrpcpb.ScanResponse), err
}

func (server *Server) KvPrewrite(_ context.Context, req *kvrpcpb.PrewriteRequest) (*kvrpcpb.PrewriteResponse, error) {
	response, err := command.ExecuteNonQuery(command.NewPreWriteCommand(req), server.storage, server.Latches)
	if err != nil {
		if response, err = regionError(new(kvrpcpb.PrewriteResponse), err); err != nil {
			return nil, err
		}
	}
	return response.(*kvrpcpb.PrewriteResponse), err
}

func (server *Server) KvCommit(_ context.Context, req *kvrpcpb.CommitRequest) (*kvrpcpb.CommitResponse, error) {
	response, err := command.ExecuteNonQuery(command.NewCommitCommand(req), server.storage, server.Latches)
	if err != nil {
		if response, err = regionError(new(kvrpcpb.CommitResponse), err); err != nil {
			return nil, err
		}
	}
	return response.(*kvrpcpb.CommitResponse), err
}

func (server *Server) KvCheckTxnStatus(_ context.Context, req *kvrpcpb.CheckTxnStatusRequest) (*kvrpcpb.CheckTxnStatusResponse, error) {
	response, err := command.ExecuteNonQuery(command.NewCheckTxnStatusCommand(req), server.storage, server.Latches)
	if err != nil {
		if response, err = regionError(new(kvrpcpb.CheckTxnStatusResponse), err); err != nil {
			return nil, err
		}
	}
	return response.(*kvrpcpb.CheckTxnStatusResponse), err
}

func (server *Server) KvBatchRollback(_ context.Context, req *kvrpcpb.BatchRollbackRequest) (*kvrpcpb.BatchRollbackResponse, error) {
	response, err := command.ExecuteNonQuery(command.NewBatchRollbackCommand(req), server.storage, server.Latches)
	if err != nil {
		if response, err = regionError(new(kvrpcpb.BatchRollbackResponse), err); err != nil {
			return nil, err
		}
	}
	return response.(*kvrpcpb.BatchRollbackResponse), err
}

func (server *Server) KvResolveLock(_ context.Context, req *kvrpcpb.ResolveLockRequest) (*kvrpcpb.ResolveLockResponse, error) {
	response, err := command.ExecuteNonQuery(command.NewResolveLockCommand(req), server.storage, server.Latches)
	if err != nil {
		if response, err = regionError(new(kvrpcpb.ResolveLockResponse), err); err != nil {
			return nil, err
		}
	}
	return response.(*kvrpcpb.ResolveLockResponse), err
}

func regionError(resp interface{}, err error) (interface{}, error) {
	if regionError, ok := err.(*raft_storage.RegionError); ok {
		respValue := reflect.ValueOf(resp)
		respValue.FieldByName("RegionError").Set(reflect.ValueOf(regionError.RequestErr))
		return resp, nil
	}
	return nil, err
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
