<!-- TOC -->

- [解题思路](#解题思路)
    - [lab3a](#lab3a)
    - [lab3b](#lab3b)
    - [lab3c](#lab3c)
- [相关知识学习](#相关知识学习)
    - [Multi-Raft](#multi-raft)
    - [负载均衡](#负载均衡)

<!-- /TOC -->

## 解题思路

### lab3a

本部分主要涉及 Raft 算法 leader transfer 和 conf change 功能的两个工作，主要涉及修改的代码文件是 raft.go

对于 leader transfer，注意以下几点即可：
* leader 在 transfer 时需要阻写。
* 当 leader 发现 transferee 的 matchIndex 与本地的 lastIndex 相等时直接发送 timeout 请求让其快速选举即可，否则继续发送日志让其快速同步。
* 当 follower 收到 leader transfer 请求时，直接发起选举即可

对于 conf change，注意以下几点即可：
* 只对还在共识组配置中的 raftnode 进行 tick。
* 新当选的 leader 需要保证之前任期的所有 log 都被 apply 后才能进行新的 conf change 变更，这有关 raft 单步配置变更的 safety，可以参照 [邮件](https://groups.google.com/g/raft-dev/c/t4xj6dJTP6E/m/d2D9LrWRza8J) 和相关 [博客](https://zhuanlan.zhihu.com/p/342319702)。
* 只有当前共识组的最新配置变更日志被 apply 后才可以接收新的配置变更日志。
* 增删节点时需要维护 PeerTracker。

### lab3b

### lab3c

本部分主要涉及对收集到的心跳信息进行选择性维护和对 balance-region 策略的具体实现两个工作，主要涉及修改的代码文件是 cluster.go 和 balance_region.go

对于维护心跳信息，按照以下流程执行即可：
* 判断是否存在 epoch，若不存在则返回 err
* 判断是否存在对应 region，如存在则判断 epoch 是否陈旧，如陈旧则返回 err；若不存在则选择重叠的 regions，接着判断 epoch 是否陈旧。
* 否则维护 region 并更新 store 的 status 即可。

对于 balance-region 策略的实现，按照以下步骤执行即可：
* 获取健康的 store 列表：
    * store 必须状态是 up 且最近心跳的间隔小于集群判断宕机的时间阈值。
    * 如果列表长度小于等于 1 则不可调度，返回空即可。
    * 按照 regionSize 对 store 大小排序。
* 寻找可调度的 store：
    * 按照大小在所有 store 上从大到小依次寻找可以调度的 region，优先级依次是 pending，follower，leader。
    * 如果能够获取到 region 且 region 的 peer 个数等于集群的副本数，则说明该 region 可能可以在该 store 上被调度走。
* 寻找被调度的 store：
    * 按照大小在所有 store 上从小到达依次寻找不存在该 region 的 store。
    * 找到后判断迁移是否有价值，即两个 store 的大小差值是否大于 region 的两倍大小，这样迁移之后其大小关系依然不会发生改变。
* 如果两个 store 都能够寻找到，则在新 store 上申请一个该 region 的 peer，创建对应的 MovePeerOperator 即可。

## 相关知识学习

### Multi-Raft

Multi-Raft 是分布式 KV 可以 scale out 的基石。TiKV 对每个 region 的 conf change 和 transfer leader 功能能够将 region 动态的在所有 store 上进行负载均衡，对 region 的 split 和 merge 则是能够解决单 region 热点并无用工作损耗资源的问题。不得不说，后两者尽管道理上理解起来很简单，但工程实现上有太多细节要考虑了（据说贵司写了好几年才稳定），分析可能的异常情况实在是太痛苦了，为贵司能够啃下这块硬骨头点赞。

最近看到有一个基于 TiKV 的 hackathon [议题](https://github.com/TPC-TiKV/rfc)，其本质是想通过更改线程模型来优化 TiKV 的写入性能、性能稳定性和自适应能力。这里可以简单提提一些想法，其实就我们在时序数据库方向的一些经验来说，每个 TSM（TimeSeries Merge Tree）大概能够用满一个核的 CPU 资源。只要我们将 TSM 引擎额个数与 CPU 核数绑定，写入性能基本是能够随着核数增加而线性提升的。那么对于 KV 场景，是否开启 CPU 个数的 LSM 引擎能够更好的利用 CPU 资源呢？即对于 raftstore，是否启动 CPU 个数的 Rocksdb 实例能够更好的利用资源呢？感觉这里也可以做做测试尝试一下。

### 负载均衡

负载均衡是分布式系统中的一大难题，不同系统均有不同的策略实现，不同的策略可能在不同的 workload 中更有效。

相比 pd 的实现，我们在 lab3c 实现的策略实际上很 trivial，因此我们简单学习了 pd 调度 region 的 [策略](https://asktug.com/t/topic/242808)。尽管这些策略道理上理解起来都比较简单，但如何将所有统计信息准确的量化成一个动态模型却是一件很难尽善尽美的事，这中间的很多指标也只能是经验值，没有严谨的依据。

有关负载均衡我们对学术界的相关工作还不够了解，之后有时间会进行一些关注。