<!-- TOC -->

- [解题思路](#解题思路)
    - [lab4a](#lab4a)
    - [lab4b](#lab4b)
    - [lab4c](#lab4c)
    - [代码结构](#代码结构)
- [相关知识学习](#相关知识学习)

<!-- /TOC -->
## 解题思路

本 Lab 整体相对简单，在基本了解 MVCC, 2PC 和 Percolator 后便可动手了，面向测试用例编程即可。

### lab4a

本部分是对 mvcc 模块的实现，涉及修改的代码文件主要涉及到 transaction.go。需要利用对 CFLock, CFDefault 和 CFWrite 三个 CF 的一些操作来实现 mvcc。

针对 Lock 相关的函数：
* PutLock：将 PUT <key, lock.ToBytes()> 添加到 Modify 即可。
* DeleteLock：将 Delete <key> 添加到 Modify 即可。
* GetLock：在 CFLock 中查找即可。

针对 Value 相关的函数：
* PutValue：将 PUT <EncodeKey(key, txn.StartTS), value> 添加到 Modify 即可。
* DeleteValue：将 Delete <EncodeKey(key, txn.StartTS)> 添加到 Modify 即可。
* GetValue：首先从 CFWrite 中寻找在当前快照之前已经提交的版本。如果未找到则返回空，如果找到则正对不同的 Kind 有不同的行为：
    * Put：根据 value 中的 StartTS 去 CFDefault 寻找即可。
    * Delete：返回空即可。
    * Rollback：继续寻找之前的版本。

针对 Write 相关的函数：
* PutWrite：将 PUT <EncodeKey(key, commitTS), write.ToBytes()> 添加到 Modify 即可。
* CurrentWrite：从 CFWrite 当中寻找当前 key 对应且值的 StartTS 与当前事务 StartTS 相同的行。
* MostRecentWrite：从 CFWrite 当中寻找当前 key 对应且值的 StartTS 最大的行。

### lab4b

本部分是对 Percolator 算法 KVPreWrite, KVCommit 和 KVGet 三个方法的实现，涉及修改的代码文件主要涉及到 server.go, query.go 和 nonquery.go。

* KVPreWrite：针对每个 key，首先检验是否存在写写冲突，再检查是否存在行锁，如存在则需要根据所属事务是否一致来决定是否返回 KeyError，最后将 key 添加到 CFDefault 和 CFLock 即可。
* KVCommit：针对每个 key，首先检查是否存在行锁，如不存在则已经 commit 或 rollback，如存在则需要根据 CFWrite 中的当前事务状态来判断是否返回 KeyError，最后将 key 添加到 CFWrite 中并在 CFLock 中删除即可。
* KVGet：首先检查行锁，如为当前事务所锁，则返回 Error，否则调用 mvcc 模块的 GetValue 获得快照读即可。

### lab4c

本部分是对 Percolator 算法 KvCheckTxnStatus, KvBatchRollback, KvResolveLock 和 KvScan 四个方法的实现，涉及修改的代码文件主要涉及到 server.go, query.go 和 nonquery.go。

* KvCheckTxnStatus：检查 PrimaryLock 的行锁，如果存在且被当前事务锁定，则根据 ttl 时间判断是否过期从而做出相应的动作；否则锁很已被 rollback 或者 commit，从 CFWrite 中获取相关信息即可。
* KvBatchRollback：针对每个 key，首先检查是否存在行锁，如果存在则删除 key 在 CFLock 和 CFValue 中的数并且在 CFWrite 中写入一条 rollback 即可。如果不存在或者不归当前事务锁定，则从 CFWrite 中获取当前事务的提交信息，如果不存在则向 CFWrite 写入一条 rollback，如果存在则根据是否为 rollback 判断是否返回错误。
* KvResolveLock：针对每个 key，根据请求中的参数决定来 commit 或者 rollback 即可。
* KvScan：利用 Scanner 扫描到没有 key 或达到 limit 阈值即可。针对 scanner，需要注意不能读有锁的 key，不能读未来的版本，不能读已删除或者已 rollback 的 key。

### 代码结构

为了使得 server.go 逻辑代码清晰，在分别完成三个 lab 后对代码进行了进一步整理，针对读写请求分别抽象出来了接口，这样可以使得逻辑更为清晰。

```Go
type BaseCommand interface {
	Context() *kvrpcpb.Context
	StartTs() uint64
}

type Base struct {
	context *kvrpcpb.Context
	startTs uint64
}

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
```

## 相关知识学习

有关分布式事务，我们之前有过简单的 [学习](https://tanxinyu.work/distributed-transactions/)，对 2PL, 2PC 均有简单的了解，因此此次在实现 Percolator 时只需要关注 2PC 与 MVCC 的结合即可，这里重点参考了以下博客：
* [TiKV 源码解析系列文章（十二）分布式事务](https://zhuanlan.zhihu.com/p/77846678)
* [TiKV 事务模型概览，Google Spanner 开源实现](https://pingcap.com/zh/blog/tidb-transaction-model)
* [Google Percolator 分布式事务实现原理解读](http://mysql.taobao.org/monthly/2018/11/02/)
* [Async Commit 原理介绍](https://pingcap.com/zh/blog/async-commit-principle)

实现完后，我们进一步被 Google 的聪明所折服，Percolator 基于单行事务实现了多行事务，基于 MVCC 实现了 SI 隔离级别。尽管其事务恢复流程相对复杂，但其本质上是在 CAP 定理中通过牺牲恢复时的 A 来优化了协调者正常写入时的 A，即协调者单点在 SQL 层不用高可用来保证最终执行 commit 或者 abort。因为一旦协调者节点挂掉，该事务在超过 TTL （TTL 的超时也是由 TSO 的时间戳来判断，对于各个 TiKV 节点来说均为逻辑时钟，这样的设计也避免了 Wall Clock 的同步难题）后会被其他事务 rollback，总体上来看 Percolator 比较优雅的解决了 2PC 的 safety 问题。

当然，分布式事务可以深究的地方还很多，并且很多思想都与 Lamport 那篇最著名的论文 [`Time, Clocks, and the Ordering of Events in a Distributed System`](https://tanxinyu.work/time-clock-order-in-distributed-system-thesis/) 有关。除了 TiDB 外，Spanner，YugaByte，CockroachDB 等 NewSQL 数据库均有自己的大杀器，比如 TrueTime，HLC 等等。总之这块儿挺有意思的，虽然在这儿告一段落，但希望以后有机会能深入做一些相关工作。