package command

import "github.com/pingcap-incubator/tinykv/proto/pkg/kvrpcpb"

type BaseCommand interface {
	Context() *kvrpcpb.Context
	StartTs() uint64
}

type Base struct {
	context *kvrpcpb.Context
	startTs uint64
}

func NewBase(context *kvrpcpb.Context, startTs uint64) Base {
	return Base{
		context: context,
		startTs: startTs,
	}
}

func (base Base) Context() *kvrpcpb.Context {
	return base.context
}

func (base Base) StartTs() uint64 {
	return base.startTs
}
