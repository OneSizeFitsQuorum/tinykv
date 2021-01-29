package server

import (
	"context"
	"github.com/pingcap-incubator/tinykv/proto/pkg/kvrpcpb"
	"github.com/pingcap-incubator/tinykv/kv/storage"
)

// The functions below are Server's Raw API. (implements TinyKvServer).
// Some helper methods can be found in sever.go in the current directory

// RawGet return the corresponding Get response based on RawGetRequest's CF and Key fields
func (server *Server) RawGet(_ context.Context, req *kvrpcpb.RawGetRequest) (*kvrpcpb.RawGetResponse, error) {
	reader, err := server.storage.Reader(req.Context)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	result, err := reader.GetCF(req.Cf, req.Key)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return &kvrpcpb.RawGetResponse{
			NotFound: true,
		}, nil
	}
	return &kvrpcpb.RawGetResponse{
		NotFound: false,
		Value:    result,
	}, nil
	return nil, nil
}

// RawPut puts the target data into storage and returns the corresponding response
func (server *Server) RawPut(_ context.Context, req *kvrpcpb.RawPutRequest) (*kvrpcpb.RawPutResponse, error) {
	mod := []storage.Modify{{
		Data: storage.Put{
			Key:   req.Key,
			Value: req.Value,
			Cf:    req.Cf,
		},
	}}
	err := server.storage.Write(req.Context, mod)
	if err != nil {
		return nil, err
	}
	return &kvrpcpb.RawPutResponse{}, nil
}

// RawDelete delete the target data from storage and returns the corresponding response
func (server *Server) RawDelete(_ context.Context, req *kvrpcpb.RawDeleteRequest) (*kvrpcpb.RawDeleteResponse, error) {
	mod := []storage.Modify{{
		Data: storage.Delete{
			Key: req.Key,
			Cf:  req.Cf,
		},
	}}
	err := server.storage.Write(req.Context, mod)
	if err != nil {
		return nil, err
	}
	return &kvrpcpb.RawDeleteResponse{}, nil
}

// RawScan scan the data starting from the start key up to limit. and return the corresponding result
func (server *Server) RawScan(_ context.Context, req *kvrpcpb.RawScanRequest) (*kvrpcpb.RawScanResponse, error) {
	reader, err := server.storage.Reader(req.Context)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	iter := reader.IterCF(req.Cf)
	defer iter.Close()
	var count uint32 = 0
	results := make([]*kvrpcpb.KvPair, 0)
	for iter.Seek(req.StartKey); iter.Valid() && count < req.Limit; iter.Next() {
		key := iter.Item().Key()
		value, err := iter.Item().Value()
		if err != nil {
			return nil, err
		}
		results = append(results, &kvrpcpb.KvPair{
			Key:   key,
			Value: value,
		})
		count++
	}
	return &kvrpcpb.RawScanResponse{Kvs: results}, nil
}
