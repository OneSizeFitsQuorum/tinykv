<!-- TOC -->

- [解题思路](#解题思路)
    - [lab2a](#lab2a)
        - [Leader election](#leader-election)
        - [Log replication](#log-replication)
        - [Implement the raw node interface](#implement-the-raw-node-interface)
    - [lab2b](#lab2b)
        - [Implement peer storage](#implement-peer-storage)
        - [Implement Raft ready process](#implement-raft-ready-process)
    - [lab2c](#lab2c)
        - [Implement in Raft](#implement-in-raft)
        - [Implement in raftstore](#implement-in-raftstore)
- [相关知识学习](#相关知识学习)
    - [Raft](#raft)
    - [KVRaft](#kvraft)
    - [Snapshot](#snapshot)

<!-- /TOC -->

## 解题思路

### lab2a

#### Leader election

本部分是对 raft 模块 leader 选举功能的实现，涉及修改的代码文件主要涉及到 raft.go、log.go

raft 模块 leader 选举流程如下：

![](../imgs/solution/leader%20election.jpg)

第一步，我们首先实现对 raft 的初始化。

实现 log.go 中的 newLog 方法，调用 storage 的 InitialState 等方法对 RaftLog 进行初始化，读取持久化在 storage 中 term、commit、vote 和 entries，为后面的 lab 做准备。完成 RaftLog 的初始化后，再填充 Raft 中的相应字段，即完成 Raft 对象的初始化。

第二步，我们实现 Raft 对象的 tick() 函数

上层应用会调用 tick() 函数，作为逻辑时钟控制 Raft 模块的选举功能和心跳功能。因此我们实现 tick() 函数，当 Raft 状态是 Follwer 时，检查自上次接收心跳之后，间隔时间是否超过了 election timeout，如果超过了，将发送 MessageType_MsgHup；当 Raft 状态时 Leader 时，检查自上次发送心跳之后，间隔时间是否超过了 heartbeat timeout，如果超过了，将发送 MessageType_MsgBeat。

第三步，我们实现 raft.Raft.becomeXXX 等基本函数

实现了 becomeFollower(),becomeCandidate(),becomeLeader() 等 stub 函数，对不同状态下的属性进行赋值。

第四步，我们实现 Step() 函数对不同 Message 的处理

主要涉及到的 Message 有

- MessageType_MsgHup

- MessageType_MsgRequestVote

- MessageType_MsgRequestVoteResponse

接下来分情况实现：

（1）MessageType_Msgup

当 Raft 状态为 Follower 和 Candidate 时，会先调用 becomeCandidate() 方法，将自己的状态转变为 Candidate，然后向所有 peer 发送 MessageType_MsgRequestVote 消息，请求他们的投票

（2）MessageType_MsgRequestVote

当 Raft 接收到此消息时，会在以下情况拒绝投票：

- 当 Candidate 的 term 小于当前 raft 的 term 时拒绝投票

- 如果当前 raft 的 term 与 candidate 的 term 相等，但是它之前已经投票给其他 Candidate 时，会拒绝投票

- 如果当前 raft 发现 candidate 的日志不如自己的日志更 up-to-date 时，也会拒绝投票

（3）MessageType_MsgRequestVoteResponse

Candidate 接收到此消息时，就会根据消息的 reject 属性来确定自己的得票，当自己的得票数大于一半以上，就会调用 becomeLeader() 函数，将状态转变为 Leader；当拒绝票数也大于一半以上时，就会转回到 Follower 状态。

#### Log replication

本部分是对 raft 模块日志复制功能的实现，涉及修改的代码文件主要涉及到 raft.go、log.go

日志复制的流程如下：

![Log Replication](../imgs/solution/log%20replication.jpg)

本部分主要实现不同状态的 raft 对以下 Message 的处理：

- MessageType_MsgBeat
- MessageType_MsgHeartbeat
- MessageType_MsgHeartbeatResponse
- MessageType_MsgPropose
- MessageType_MsgAppend
- MessageType_MsgAppendResponse

接下来分情况实现：

（1）MessageType_MsgBeat

当上层应用调用 tick() 函数时，Leader 需要检查是否到了该发送心跳的时候，如果到了，那么就发送 MessageType_MsgHeartbeat。

leader 会将自己的 commit 值赋给在 MsgHeartbeat 消息中响应值，以让 Follower 能够及时 commit 安全的 entries

（2）MessageType_MsgHeartbeat

当 Follower 接收到心跳时，会更新自己的 electionTimeout，并会将自己的 lastIndex 与 leader 的 commit 值比较，让自己能够及时 commit entry。

（3）MessageType_MsgHeartbeatResponse

当 Leader 接收到心跳回复时，会比较对应 Follower 的 Pr.Match, 如果发现 Follower 滞后，就会向其发送缺少的 entries

 (4)MessageType_MsgPropose

当 Leader 要添加 data 到自己的 log entries 中时，会发送一个 local message—MsgPropose 来让自己向所有 follower 同步 log entries，发送 MessageType_MsgAppend

（5）MessageType_MsgAppend

当 Follower 接收到此消息时，会在以下情况拒绝 append：

- 当 Leader 的 term 小于当前 raft 的 term 时拒绝 append
- 当 Follower 在对应 Index 处不含 entry，说明 Follower 滞后比较严重
- 当 Follower 在对应 Index 处含有 entry，但是 term 不相等，说明产生了冲突

其他情况，Follower 会接收新的 entries，并更新自己的相关属性。

（6）MessageType_MsgAppendResponse

当 Leader 发现 Follower 拒绝 append 后，会更新 raft.Prs 中对应 Follower 的进度信息，并根据新的进度，重新发送 entries。

#### Implement the raw node interface

本部分主要实现 raw node 的接口，涉及修改的代码文件为 rawnode.go

RawNode 对象中的属性除了 Raft 对象，还增加了 prevSoftState 和 preHardState 两个属性，用于在 HasReady() 函数中判断 node 是否 pending

此外还实现了 Advance() 函数，主要是对 Raft 内部属性进行更新。

### lab2b

#### Implement peer storage

本部分主要实现 peer_storage.go 中 SaveReadyState() 方法和 Append() 方法，涉及修改的代码文件为 peer_storage.go

peer storage 除了管理持久化 raft log 外，也会管理持久化其他元数据（RaftLocalState、RaftApplyState 和 RegionLocalState），因此我们需要实现 SaveReadyState() 方法，将 raft.Ready 中修改过的状态和数据保存到 badger 中。

首先我们通过实现 Append() 方法，保存需要持久化的 raft log。遍历 Ready 中 Entries，调用 SetMeta() 方法将他们保存到 raftWB，并删除可能未提交的 raft log，最后更新 raftState。

在处理完 raft log 后，我们还需要保存 Ready 中的 hardState，并在最后调用 WriteToDB() 方法保证之前的修改落盘。

#### Implement Raft ready process

本部分主要实现 peer_storage_handler.go 中的 proposeRaftCommand() 和 HandleRaftReady() 方法，涉及修改的代码文件为 peer_storage_handler.go

proposeRaftCommand() 方法使得系统有能力将接收到的 client 请求通过 raft 模块进行同步，以实现分布式环境下的一致性。在本方法中，我们直接调用 raft 模块的 Propose 方法，将 client 请求进行同步，并为该请求初始化对应的 proposal，以便该请求 committed 后将结果返回给 client

当 msg 被 raft 模块处理后，会导致 raft 模块的一些状态变化，这时候需要 HandleRaftReady() 方法进行一些操作来处理这些变化：

1. 需要调用 peer_storage.go() 中的 SaveReadyState() 方法，将 log entries 和一些元数据变化进行持久化。
2. 需要调用 peer_storage_handler 中的 send() 方法，将一些需要发送的消息，发送给同一个 region 中的 peer
3. 我们需要处理一些 committed entries，将他们应用到状态机中，并把结果通过 callback 反馈给 client
4. 在上述处理完后，需要调用 advance() 方法，将 raft 模块整体推进到下一个状态

### lab2c

因为 raft entries 不可能一直无限增长下去，所以本部分我们需要实现 snapshot 功能，清理之前的 raft entries。

整个 lab2c 的执行流程如下：

1. gc log 的流程：

![gc raftLog](../imgs/solution/gc%20raftLog.png)

2. 发送和应用 snapshot 的流程：

![send and apply snapshot](../imgs/solution/send%20and%20apply%20Snapshot.png)

#### Implement in Raft

当 leader 发现 follower 落后太多时，会主动向 follower 发送 snapshot，对其进行同步。在 Raft 模块内部，需要增加对 MessageType_MsgSnapshot 消息的处理，主要对以下两点进行处理：

1. 当 leader 需要向 follower 同步日志时，如果同步的日志已经被 compact 了，那么直接发送 snapshot 给 follower 进行同步，否则发送 MessageType_MsgAppend 消息，向 follower 添加 entries。通过调用 peer storage 的 Snapshot() 方法，我们可以得到已经制作完成的 snapshot
2. 实现 handleSnapshot() 方法，当 follower 接收到 MessageType_MsgSnapshot 时，需要进行相应处理。

在第二步中，follower 需要判断 leader 发送的 snapshot 是否会与自己的 entries 产生冲突，如果发送的 snapshot 是目前现有 entries 的子集，说明 snapshot 是 stale 的，那么要返回目前 follower 的进度，更新 leader 中相应的 Match 和 Next，以便再下一次发送正确的日志；如果没有发生冲突，那么 follower 就根据 snapshot 中的信息进行相应的更新，更新自身的 committed 等 index，如果 confstate 也产生变化，有新的 node 加入或者已有的 node 被移除，需要更新本节点的 confState，为 lab3 做准备。

#### Implement in raftstore

在本部分中，当日志增长超过 RaftLogGcCountLimit 的限制时，会要求本节点整理和删除已经应用到状态机的旧日志。节点会接收到类似于 Get/Put/Delete/Snap 命令的 CompactLogRequest，因此我们需要在 lab2b 的基础上，当包含 CompactLogRequest 的 entry 提交后，增加 processAdminRequest() 方法来对这类 adminRequest 的处理。

在 processAdminRequest() 方法中，我们需要更新 RaftApplyState 中 RaftTruncatedState 中的相关元数据，记录最新截断的最后一个日志的 index 和 term，然后调用 ScheduleCompactLog() 方法，异步让 RaftLog-gc worker 能够进行旧日志删除的工作。

另外，因为 raft 模块在处理 snapshot 相关的 msg 时，也会对一些状态进行修改，所以在 peer_storage.go 方法中，我们需要在 SaveReadyState() 方法中，调用 ApplySnapshot() 方法中，对相应的元数据进行保存。

在 ApplySnapshot() 方法中，如果当前节点已经处理过的 entries 只是 snapshot 的一个子集，那么需要对 raftLocalState 中的 commit、lastIndex 以及 raftApplyState 中的 appliedIndex 等元数据进行更新，并调用 ClearData() 和 ClearMetaData() 方法，对现有的 stale 元数据以及日志进行清空整理。同时，也对 regionLocalState 进行相应更新。最后，我们需要通过 regionSched 这个 channel，将 snapshot 应用于对应的状态机

## 相关知识学习

### Raft

Raft 是 2015 年以来最受人瞩目的共识算法，有关其前世今生可以参考我们总结的 [博客](https://tanxinyu.work/raft/)，此处不再赘述。

etcd 是一个生产级别的 Raft 实现，我们在实现 lab2a 的时候大量参考了 etcd 的代码。这个过程不仅帮助我们进一步了解了 etcd 的 codebase，也让我们进一步意识到一个工程级别的 raft 实现需要考虑多少 corner case。整个学习过程收获还是很大的，这里贴一些 etcd 的优质博客以供学习。
* [etcd Raft 库解析](https://www.codedump.info/post/20180922-etcd-raft/)
* [Etcd 存储的实现](https://www.codedump.info/post/20181125-etcd-server/)
* [Etcd Raft 库的工程化实现](https://www.codedump.info/post/20210515-raft)
* [Etcd Raft 库的日志存储](https://www.codedump.info/post/20210628-etcd-wal/)

### KVRaft

在 Raft 层完成后，下一步需要做的便是基于 Raft 层搭建一个高可用的 KV 层。这里依然参考了 etcd KV 层驱动 Raft 层的方式。
即总体的思路如下所示：
```Go
for {
  select {
  case <-s.Ticker:
    Node.Tick()
  default:
    if Node.HasReady() {
      rd := Node.Ready()
      saveToStorage(rd.State, rd.Entries, rd.Snapshot)
      send(rd.Messages)
      for _, entry := range rd.CommittedEntries {
        process(entry)
      }
      s.Node.Advance(rd)
    }
}
```

做过 tinykv 的同学应该都能够感觉到 lab2b 的难度与之前有一个大 gap，我认为主要原因是需要看的代码实现是太多了。

如今回首，建议分三个步骤来做，这样效率可能会高一些：
* 了解读写流程的详细步骤。对于 client 的请求，其处理和回复均在 raft_server.go 中进行了处理，然而其在服务端内部的生命周期如何，这里需要知根知底。（注意在遇到 channel 打断同步的执行流程时不能瞎猜，一定要明确找到 channel 的接收端和发送端继续把生命周期理下去）
* 仔细阅读 raft_server.go, router.go, raftstore.go, raft_worker.go, peer_storage.go, peer_msg_handle.go 等文件的代码。这会对了解整个系统的 codebase 十分有帮助。
* 仔细阅读 tinykv 的 lab2 文档，了解编码，存储等细节后便可以动手实现了。

在实现 lab2b 中，由于时间有限，我们重点关注了 batching 的优化和 apply 时的 safety，以下进行简单的介绍：

* batching 优化：客户端发来的一条 command 可能包含多个读写请求，服务端可以将其打包成一条或多条 raft 日志。显然，打包成一条 Raft 日志的性能会更高，因为这样能够节省大量 IO 资源的消耗。当然这也需要在 apply 时对所有的 request 均做相应的业务和容错处理。

* apply 时的 safety：要想实现基于 Raft 的 KV 服务，一大难点便是如何保证 applyIndex 和状态机数据的原子性。比如在 6.824 的框架中，Raft 层对于上层状态机的假设是易失的，即重启后状态机为空，那么 applyIndex 便可以不被持久化记录，因为一旦发生重启 Raft 实例可以从 0 开始重新 apply 日志，对于状态机来说这个过程保证不会重复。然而这样的实现虽然保证了 safety，但却不是一个生产可用的实现。对于 Tinykv，其状态机为非易失的 LSM 引擎，一旦要记录 applyIndex 就可能出现与状态机数据不一致的原子性问题，即重启后可能会存在日志被重复 apply 到状态机的现象。为了解决这一问题，我们将每个 Index 下 entry 的应用和对应 applyIndex 的更新放到了一个事务中来保证他们之间的原子性，巧妙地解决了该过程的 safety 问题。

### Snapshot

Tinykv 的 Snapshot 几乎是一个纯异步的方案，在架构上有很多讲究，这里可以仔细阅读文档和一位社区同学分享的 [Snapshot 流程](https://asktug.com/t/topic/273859) 后再开始编码。

一旦了解了以下两个流程，代码便可以自然而然地写出来了。
* log gc 流程
* snapshot 的异步生成，异步分批发送，异步分批接收和异步应用。