# talent plant lab2 解题思路

## lab2a

### Leader election

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

- 如果当前 raft 发现 candidate 的日志不如自己的日志更 up-to-date 时，也会拒绝投票（提前为 lab2ab 写）

（3）MessageType_MsgRequestVoteResponse

Candidate 接收到此消息时，就会根据消息的 reject 属性来确定自己的得票，当自己的得票数大于一半以上，就会调用 becomeLeader() 函数，将状态转变为 Leader；当拒绝票数也大于一半以上时，就会转回到 Follower 状态。

### Log replication

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

### Implement the raw node interface

本部分主要实现 raw node 的接口，涉及修改的代码文件为 rawnode.go

RawNode 对象中的属性除了 Raft 对象，还增加了 prevSoftState 和 preHardState 两个属性，用于在 HasReady() 函数中判断 node 是否 pending

此外还实现了 Advance() 函数，主要是对 Raft 内部属性进行更新。

________

## lab2b

### Implement peer storage

<br>
本部分主要实现peer_storage.go中SaveReadyState()方法和Append()方法，涉及修改的代码文件为peer_storage.go

peer storage除了管理持久化raft log外，也会管理持久化其他元数据（RaftLocalState、RaftApplyState和RegionLocalState），因此我们需要实现SaveReadyState()方法，将raft.Ready中修改过的状态和数据保存到badger中。

首先我们通过实现Append()方法，保存需要持久化的raft log。遍历Ready中Entries，调用SetMeta()方法将他们保存到raftWB，并删除之前产生冲突的raft log，最后更新raftState。

在处理完raft log后，我们还需要保存Ready中的hardState，并在最后调用WriteToDB()方法保证之前的修改落盘。

<br>

### Implement Raft ready process
<br>

本部分主要实现peer_storage_handler.go中的proposeRaftCommand()和 HandleRaftReady()方法，涉及修改的代码文件为peer_storage_handler.go

proposeRaftCommand()方法使得系统有能力将接收到的client请求通过raft模块进行同步，以实现分布式环境下的一致性。在本方法中，我们直接调用raft模块的Propose方法，将client请求进行同步，并为该请求初始化对应的proposal，以便该请求committed后将结果返回给client

当msg被raft模块处理后，会导致raft模块的一些状态变化，这时候需要HandleRaftReady()方法进行一些操作来处理这些变化：

1. 需要调用peer_storage.go()中的SaveReadyState()方法，将log entries和一些元数据变化进行持久化。
2. 需要调用peer_storage_handler中的send()方法，将一些需要发送的消息，发送给同一个region中的peer
3. 我们需要处理一些committed entries，将他们应用到状态机中，并把结果通过callback反馈给client
4. 在上述处理完后，需要调用advance()方法，将raft模块整体推进到下一个状态

特别地，在第三步处理committed entries时，我们声明一个processNormalRequest()方法来处理Get/Put/Delete/Snap等请求





## lab2c

<br>
因为raft entries不可能一直无限增长下去，所以本部分我们需要实现snapshot功能，清理之前的raft entries。

整个lab2c的执行流程如下：

1. gc log的流程如下:
   1. ![gc raftLog](../imgs/solution/gc%20raftLog.png)

2. 发送和应用snapshot的流程
   1. ![send and apply snapshot](../imgs/solution/send%20and%20apply%20Snapshot.png)



### Implement in Raft

<br>
当leader发现follower落后太多时，会主动向follower发送snapshot，对其进行同步。在Raft模块内部，需要增加对MessageType_MsgSnapshot消息的处理，主要对以下两点进行处理：

1. 当leader需要向follower同步日志时，如果同步的日志已经被compact了，那么直接发送snapshot给follower进行同步，否则发送MessageType_MsgAppend消息，向follower添加entries。通过调用peer storage的Snapshot()方法，我们可以得到已经制作完成的snapshot
2. 实现handleSnapshot()方法，当follower接收到MessageType_MsgSnapshot时，需要进行相应处理。

在第二步中，follower需要判断leader发送的snapshot是否会与自己的entries产生冲突，如果发送的snapshot是目前现有entries的子集，说明snapshot是stale的，那么要返回目前follower的进度，更新leader中相应的Match和Next，以便再下一次发送正确的日志；如果没有发生冲突，那么follower就根据snapshot中的信息进行相应的更新，更新自身的committed等index，如果confstate也产生变化，有新的node加入或者已有的node被移除，需要更新本节点的confState，为lab3做准备。
<br>


### Implement in raftstore
<br>

在本部分中，当日志增长超过RaftLogGcCountLimit的限制时，会要求本节点整理和删除已经应用到状态机的旧日志。节点会接收到类似于Get/Put/Delete/Snap命令的CompactLogRequest，因此我们需要在lab2b的基础上，当包含CompactLogRequest的entry提交后，增加processAdminRequest()方法来对这类adminRequest的处理。

在processAdminRequest()方法中，我们需要更新RaftApplyState中RaftTruncatedState中的相关元数据，记录最新截断的最后一个日志的index和term，然后调用ScheduleCompactLog()方法，异步让RaftLog-gc worker能够进行旧日志删除的工作。

另外，因为raft模块在处理snapshot相关的msg时，也会对一些状态进行修改，所以在peer_storage.go方法中，我们需要在SaveReadyState()方法中，调用ApplySnapshot()方法中，对相应的元数据进行保存。

在ApplySnapshot()方法中，如果当前节点已经处理过的entries只是snapshot的一个子集，那么需要对raftLocalState中的commit、lastIndex以及raftApplyState中的appliedIndex等元数据进行更新，并调用ClearData()和ClearMetaData()方法，对现有的stale元数据以及日志进行清空整理。同时，也对regionLocalState进行相应更新。最后，我们需要通过regionSched这个channel，将snapshot应用于对应的region