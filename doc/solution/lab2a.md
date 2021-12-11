# talent plant lab2a 解题思路

## Leader election

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

## Log replication

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

## Implement the raw node interface

本部分主要实现 raw node 的接口，涉及修改的代码文件为 rawnode.go

RawNode 对象中的属性除了 Raft 对象，还增加了 prevSoftState 和 preHardState 两个属性，用于在 HasReady() 函数中判断 node 是否 pending

此外还实现了 Advance() 函数，主要是对 Raft 内部属性进行更新。
