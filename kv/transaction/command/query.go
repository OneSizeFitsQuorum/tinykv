package command

import (
	"github.com/pingcap-incubator/tinykv/kv/raftstore/util"
	"github.com/pingcap-incubator/tinykv/kv/storage"
	"github.com/pingcap-incubator/tinykv/kv/transaction/mvcc"
	"github.com/pingcap-incubator/tinykv/proto/pkg/kvrpcpb"
)

type QueryCommand interface {
	BaseCommand
	Read(txn *mvcc.MvccTxn) (interface{}, error)
}

func ExecuteQuery(cmd QueryCommand, storage storage.Storage) (interface{}, error) {
	ctx := cmd.Context()
	reader, err := storage.Reader(ctx)
	if err != nil {
		return &kvrpcpb.ScanResponse{RegionError: util.RaftstoreErrToPbError(err)}, nil
	}
	defer reader.Close()
	return cmd.Read(mvcc.NewMvccTxn(reader, cmd.StartTs()))
}

type GetCommand struct {
	Base
	req *kvrpcpb.GetRequest
}

func NewGetCommand(req *kvrpcpb.GetRequest) GetCommand {
	return GetCommand{
		Base: NewBase(req.Context, req.Version),
		req:  req,
	}
}

func (cmd GetCommand) Read(txn *mvcc.MvccTxn) (interface{}, error) {
	key := cmd.req.Key
	lock, err := txn.GetLock(key)
	if err != nil {
		return nil, err
	}
	response := new(kvrpcpb.GetResponse)
	if lock.IsLockedFor(key, txn.StartTS, response) {
		return response, nil
	}
	value, err := txn.GetValue(key)
	if err != nil {
		return nil, err
	}
	return &kvrpcpb.GetResponse{
		Value:    value,
		NotFound: value == nil,
	}, nil
}

type ScanCommand struct {
	Base
	req *kvrpcpb.ScanRequest
}

func NewScanCommand(req *kvrpcpb.ScanRequest) ScanCommand {
	return ScanCommand{
		Base: NewBase(req.Context, req.Version),
		req:  req,
	}
}

func (cmd ScanCommand) Read(txn *mvcc.MvccTxn) (interface{}, error) {
	scanner := mvcc.NewScanner(cmd.req.StartKey, txn)
	defer scanner.Close()
	response := new(kvrpcpb.ScanResponse)
	for limit := cmd.req.Limit; limit > 0; limit-- {
		key, value, err := scanner.Next()
		if err != nil {
			if keyError, ok := err.(*mvcc.KeyError); ok {
				response.Pairs = append(response.Pairs, &kvrpcpb.KvPair{Error: &kvrpcpb.KeyError{Locked: keyError.Locked}})
				continue
			} else {
				return nil, err
			}
		}
		if key == nil {
			break
		}
		response.Pairs = append(response.Pairs, &kvrpcpb.KvPair{Key: key, Value: value})
	}
	return response, nil
}
