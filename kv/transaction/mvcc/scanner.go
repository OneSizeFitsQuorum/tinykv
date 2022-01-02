package mvcc

import (
	"github.com/pingcap-incubator/tinykv/kv/util/engine_util"
	"github.com/pingcap-incubator/tinykv/proto/pkg/kvrpcpb"
)

// Scanner is used for reading multiple sequential key/value pairs from the storage layer. It is aware of the implementation
// of the storage layer and returns results suitable for users.
// Invariant: either the scanner is finished and cannot be used, or it is ready to return a value immediately.
type Scanner struct {
	it  engine_util.DBIterator
	txn *MvccTxn
}

// NewScanner creates a new scanner ready to read from the snapshot in txn.
func NewScanner(startKey []byte, txn *MvccTxn) *Scanner {
	it := txn.Reader.IterCF(engine_util.CfWrite)
	it.Seek(EncodeKey(startKey, TsMax))
	return &Scanner{
		it:  it,
		txn: txn,
	}
}

func (scan *Scanner) Close() {
	scan.it.Close()
}

// Next returns the next key/value pair from the scanner. If the scanner is exhausted, then it will return `nil, nil, nil`.
func (scan *Scanner) Next() ([]byte, []byte, error) {
	for it := scan.it; it.Valid(); {
		writeItem := it.Item()
		writeKey := writeItem.KeyCopy(nil)

		userKey := DecodeUserKey(writeKey)
		commitTs := decodeTimestamp(writeKey)

		lock, err := scan.txn.GetLock(userKey)
		if err != nil {
			return nil, nil, err
		}
		if lock != nil && lock.Ts < scan.txn.StartTS {
			return nil, nil, &KeyError{kvrpcpb.KeyError{Locked: lock.Info(userKey)}}
		}

		if commitTs >= scan.txn.StartTS {
			scan.it.Seek(EncodeKey(userKey, commitTs-1))
			continue
		}

		writeValue, err := writeItem.ValueCopy(nil)
		if err != nil {
			return nil, nil, err
		}
		write, err := ParseWrite(writeValue)
		if err != nil {
			return nil, nil, err
		}
		if write.Kind != WriteKindPut {
			scan.it.Seek(EncodeKey(userKey, 0))
			continue
		}
		value, err := scan.txn.Reader.GetCF(engine_util.CfDefault, EncodeKey(userKey, write.StartTS))
		if err != nil {
			return nil, nil, err
		}
		it.Next()
		return userKey, value, nil
	}

	return nil, nil, nil
}
