package command

import (
	"fmt"
	"github.com/pingcap-incubator/tinykv/kv/raftstore/util"
	"github.com/pingcap-incubator/tinykv/kv/storage"
	"github.com/pingcap-incubator/tinykv/kv/transaction/latches"
	"github.com/pingcap-incubator/tinykv/kv/transaction/mvcc"
	"github.com/pingcap-incubator/tinykv/proto/pkg/kvrpcpb"
)

type NonQueryCommand interface {
	BaseCommand
	IsEmpty() bool
	GetEmptyResponse() interface{}
	WriteKeys(txn *mvcc.MvccTxn) ([][]byte, error)
	Write(txn *mvcc.MvccTxn) (interface{}, error)
}

func ExecuteNonQuery(cmd NonQueryCommand, storage storage.Storage, latches *latches.Latches) (interface{}, error) {
	if cmd.IsEmpty() {
		return cmd.GetEmptyResponse(), nil
	}

	ctx := cmd.Context()
	reader, err := storage.Reader(ctx)
	if err != nil {
		return &kvrpcpb.ScanResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
	}
	defer reader.Close()
	txn := mvcc.NewMvccTxn(reader, cmd.StartTs())

	keys, err := cmd.WriteKeys(txn)
	if err != nil {
		return nil, err
	}

	latches.WaitForLatches(keys)
	defer latches.ReleaseLatches(keys)

	response, err := cmd.Write(txn)
	if err != nil {
		return nil, err
	}

	err = storage.Write(ctx, txn.Writes())
	if err != nil {
		return nil, err
	}

	latches.Validation(txn, keys)

	return response, nil
}

type PreWriteCommand struct {
	Base
	req *kvrpcpb.PrewriteRequest
}

func NewPreWriteCommand(req *kvrpcpb.PrewriteRequest) *PreWriteCommand {
	return &PreWriteCommand{
		Base: NewBase(req.Context, req.StartVersion),
		req:  req,
	}
}

func (cmd *PreWriteCommand) IsEmpty() bool {
	return len(cmd.req.Mutations) == 0
}

func (cmd *PreWriteCommand) GetEmptyResponse() interface{} {
	return &kvrpcpb.PrewriteResponse{}
}

func (cmd *PreWriteCommand) WriteKeys(txn *mvcc.MvccTxn) ([][]byte, error) {
	var keys [][]byte
	for _, mutation := range cmd.req.Mutations {
		keys = append(keys, mutation.Key)
	}
	return keys, nil
}

func (cmd *PreWriteCommand) Write(txn *mvcc.MvccTxn) (interface{}, error) {
	response := new(kvrpcpb.PrewriteResponse)
	for _, mutation := range cmd.req.Mutations {
		keyError, err := cmd.HandleMutation(txn, mutation)
		if err != nil {
			return nil, err
		} else if keyError != nil {
			response.Errors = append(response.Errors, keyError)
		}
	}
	return response, nil
}

func (cmd *PreWriteCommand) HandleMutation(txn *mvcc.MvccTxn, mutation *kvrpcpb.Mutation) (*kvrpcpb.KeyError, error) {
	// check write conflict
	write, commitTs, err := txn.MostRecentWrite(mutation.Key)
	if err != nil {
		return nil, err
	}
	if commitTs >= txn.StartTS {
		return &kvrpcpb.KeyError{Conflict: &kvrpcpb.WriteConflict{
			StartTs:    txn.StartTS,
			ConflictTs: write.StartTS,
			Key:        mutation.Key,
			Primary:    cmd.req.PrimaryLock,
		}}, nil
	}

	// check key lock
	lock, err := txn.GetLock(mutation.Key)
	if err != nil {
		return nil, err
	}
	if lock != nil {
		if lock.Ts != txn.StartTS {
			// locked by other transaction
			return &kvrpcpb.KeyError{Conflict: &kvrpcpb.WriteConflict{
				StartTs:    txn.StartTS,
				ConflictTs: lock.Ts,
				Key:        mutation.Key,
				Primary:    cmd.req.PrimaryLock,
			}}, nil
		} else {
			// lock by current transaction
			return nil, nil
		}
	}

	txn.PutValue(mutation.Key, mutation.Value)
	txn.PutLock(mutation.Key, &mvcc.Lock{Primary: cmd.req.PrimaryLock, Ts: cmd.req.StartVersion, Ttl: cmd.req.LockTtl, Kind: mvcc.WriteKindFromProto(mutation.Op)})

	return nil, nil
}

type CommitCommand struct {
	Base
	req *kvrpcpb.CommitRequest
}

func NewCommitCommand(req *kvrpcpb.CommitRequest) *CommitCommand {
	return &CommitCommand{
		Base: NewBase(req.Context, req.StartVersion),
		req:  req,
	}
}

func (cmd *CommitCommand) IsEmpty() bool {
	return len(cmd.req.Keys) == 0
}

func (cmd *CommitCommand) GetEmptyResponse() interface{} {
	return &kvrpcpb.CommitResponse{}
}

func (cmd *CommitCommand) WriteKeys(txn *mvcc.MvccTxn) ([][]byte, error) {
	return cmd.req.Keys, nil
}

func (cmd *CommitCommand) Write(txn *mvcc.MvccTxn) (interface{}, error) {
	response := new(kvrpcpb.CommitResponse)
	for _, key := range cmd.req.Keys {
		keyError, err := CommitKey(cmd.req.CommitVersion, txn, key)
		if err != nil {
			return nil, err
		} else if keyError != nil {
			response.Error = keyError
			break
		}
	}
	return response, nil
}

func CommitKey(commitTs uint64, txn *mvcc.MvccTxn, key []byte) (*kvrpcpb.KeyError, error) {
	// check key lock
	lock, err := txn.GetLock(key)
	if err != nil {
		return nil, err
	}
	if lock == nil {
		return nil, nil
	}
	if txn.StartTS != lock.Ts {
		write, _, err := txn.CurrentWrite(key)
		if err != nil {
			return nil, err
		}
		if write == nil {
			// unexpected error
			return &kvrpcpb.KeyError{Retryable: fmt.Sprintf("key %v has been locked for startTs %v", key, lock.Ts)}, nil
		} else {
			// stale message, transaction already committed
			return nil, nil
		}
	}

	txn.PutWrite(key, commitTs, &mvcc.Write{StartTS: txn.StartTS, Kind: lock.Kind})
	txn.DeleteLock(key)

	return nil, nil
}

type CheckTxnStatusCommand struct {
	Base
	req *kvrpcpb.CheckTxnStatusRequest
}

func NewCheckTxnStatusCommand(req *kvrpcpb.CheckTxnStatusRequest) *CheckTxnStatusCommand {
	return &CheckTxnStatusCommand{
		Base: NewBase(req.Context, req.LockTs),
		req:  req,
	}
}

func (cmd *CheckTxnStatusCommand) IsEmpty() bool {
	return len(cmd.req.PrimaryKey) == 0
}

func (cmd *CheckTxnStatusCommand) GetEmptyResponse() interface{} {
	return &kvrpcpb.CheckTxnStatusResponse{}
}

func (cmd *CheckTxnStatusCommand) WriteKeys(txn *mvcc.MvccTxn) ([][]byte, error) {
	return [][]byte{cmd.req.PrimaryKey}, nil
}

func (cmd *CheckTxnStatusCommand) Write(txn *mvcc.MvccTxn) (interface{}, error) {
	key := cmd.req.PrimaryKey
	// check lock
	lock, err := txn.GetLock(key)
	if err != nil {
		return nil, err
	}
	if lock != nil && lock.Ts == cmd.req.LockTs {
		if mvcc.PhysicalTime(lock.Ts)+lock.Ttl < mvcc.PhysicalTime(cmd.req.CurrentTs) {
			// lock expired
			txn.DeleteValue(key)
			txn.DeleteLock(key)
			txn.PutWrite(key, txn.StartTS, &mvcc.Write{StartTS: txn.StartTS, Kind: mvcc.WriteKindRollback})
			return &kvrpcpb.CheckTxnStatusResponse{Action: kvrpcpb.Action_TTLExpireRollback}, nil
		} else {
			return &kvrpcpb.CheckTxnStatusResponse{Action: kvrpcpb.Action_NoAction}, nil
		}
	}
	existingWrite, commitTS, err := txn.CurrentWrite(key)
	if err != nil {
		return nil, err
	}
	if existingWrite == nil {
		// The value can't be seen, roll it back.
		txn.PutWrite(key, txn.StartTS, &mvcc.Write{StartTS: txn.StartTS, Kind: mvcc.WriteKindRollback})
		return &kvrpcpb.CheckTxnStatusResponse{Action: kvrpcpb.Action_LockNotExistRollback}, nil
	}

	if existingWrite.Kind == mvcc.WriteKindRollback {
		return &kvrpcpb.CheckTxnStatusResponse{Action: kvrpcpb.Action_NoAction}, nil
	}
	return &kvrpcpb.CheckTxnStatusResponse{Action: kvrpcpb.Action_NoAction, CommitVersion: commitTS}, nil
}

type BatchRollbackCommand struct {
	Base
	req *kvrpcpb.BatchRollbackRequest
}

func NewBatchRollbackCommand(req *kvrpcpb.BatchRollbackRequest) *BatchRollbackCommand {
	return &BatchRollbackCommand{
		Base: NewBase(req.Context, req.StartVersion),
		req:  req,
	}
}

func (cmd *BatchRollbackCommand) IsEmpty() bool {
	return len(cmd.req.Keys) == 0
}

func (cmd *BatchRollbackCommand) GetEmptyResponse() interface{} {
	return &kvrpcpb.BatchRollbackResponse{}
}

func (cmd *BatchRollbackCommand) WriteKeys(txn *mvcc.MvccTxn) ([][]byte, error) {
	return cmd.req.Keys, nil
}

func (cmd *BatchRollbackCommand) Write(txn *mvcc.MvccTxn) (interface{}, error) {
	response := new(kvrpcpb.BatchRollbackResponse)
	for _, key := range cmd.req.Keys {
		keyError, err := RollbackKey(txn, key)
		if err != nil {
			return nil, err
		} else if keyError != nil {
			response.Error = keyError
			break
		}
	}
	return response, nil
}

func RollbackKey(txn *mvcc.MvccTxn, key []byte) (*kvrpcpb.KeyError, error) {
	lock, err := txn.GetLock(key)
	if err != nil {
		return nil, err
	}
	if lock == nil || lock.Ts != txn.StartTS {
		existingWrite, commitTs, err := txn.CurrentWrite(key)
		if err != nil {
			return nil, err
		}
		if existingWrite != nil {
			if existingWrite.Kind == mvcc.WriteKindRollback {
				// already rollback
				return nil, nil
			}
			// already committed
			return &kvrpcpb.KeyError{Abort: fmt.Sprintf("%v has committed in %v", key, commitTs)}, nil
		} else {
			// The value can't be seen, roll it back.
			txn.PutWrite(key, txn.StartTS, &mvcc.Write{StartTS: txn.StartTS, Kind: mvcc.WriteKindRollback})
			return nil, nil
		}
	}

	txn.DeleteValue(key)
	txn.DeleteLock(key)
	txn.PutWrite(key, txn.StartTS, &mvcc.Write{StartTS: txn.StartTS, Kind: mvcc.WriteKindRollback})

	return nil, nil
}

type ResolveLockCommand struct {
	Base
	req   *kvrpcpb.ResolveLockRequest
	pairs []mvcc.KlPair
}

func NewResolveLockCommand(req *kvrpcpb.ResolveLockRequest) *ResolveLockCommand {
	return &ResolveLockCommand{
		Base: NewBase(req.Context, req.StartVersion),
		req:  req,
	}
}

func (cmd *ResolveLockCommand) IsEmpty() bool {
	return false
}

func (cmd *ResolveLockCommand) GetEmptyResponse() interface{} {
	return &kvrpcpb.ResolveLockResponse{}
}

func (cmd *ResolveLockCommand) WriteKeys(txn *mvcc.MvccTxn) ([][]byte, error) {
	klPair, err := mvcc.AllLocksForTxn(txn)
	if err != nil {
		return nil, err
	}
	cmd.pairs = klPair
	var result [][]byte
	for _, pair := range klPair {
		result = append(result, pair.Key)
	}
	return result, nil
}

func (cmd *ResolveLockCommand) Write(txn *mvcc.MvccTxn) (interface{}, error) {
	response := new(kvrpcpb.ResolveLockResponse)
	var keyError *kvrpcpb.KeyError
	var err error
	for _, pair := range cmd.pairs {
		if cmd.req.CommitVersion == 0 {
			keyError, err = RollbackKey(txn, pair.Key)
		} else {
			keyError, err = CommitKey(cmd.req.CommitVersion, txn, pair.Key)
		}
		if err != nil {
			return nil, err
		} else if keyError != nil {
			response.Error = keyError
			break
		}
	}
	return response, nil
}
