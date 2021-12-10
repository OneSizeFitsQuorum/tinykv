# talent plant lab2a解题思路

## Leader election

本部分是对raft模块leader选举功能的实现，涉及修改的代码文件主要涉及到raft.go、log.go

raft模块leader选举流程如下：

![](../imgs/solution/leader%20election.jpg)

第一步，我们首先实现对raft的初始化。

实现log.go中的newLog方法，调用storage的InitialState等方法对RaftLog进行初始化，读取持久化在storage中term、commit、vote和entries，为后面的lab做准备。完成RaftLog的初始化后，再填充Raft中的相应字段，即完成Raft对象的初始化。



第二步，我们实现Raft对象的tick()函数

上层应用会调用tick()函数，作为逻辑时钟控制Raft模块的选举功能和心跳功能。因此我们实现tick()函数，当Raft状态是Follwer时，检查自上次接收心跳之后，间隔时间是否超过了election timeout，如果超过了，将发送MessageType_MsgHup；当Raft状态时Leader时，检查自上次发送心跳之后，间隔时间是否超过了heartbeat timeout，如果超过了，将发送MessageType_MsgBeat。



第三步，我们实现raft.Raft.becomeXXX等基本函数

实现了becomeFollower(),becomeCandidate(),becomeLeader()等stub函数，对不同状态下的属性进行赋值。



第四步，我们实现Step()函数对不同Message的处理

主要涉及到的Message有

- MessageType_MsgHup

- MessageType_MsgRequestVote

- MessageType_MsgRequestVoteResponse

  

接下来分情况实现：

（1）MessageType_Msgup

当Raft状态为Follower和Candidate时，会先调用becomeCandidate()方法，将自己的状态转变为Candidate，然后向所有peer发送MessageType_MsgRequestVote消息，请求他们的投票

（2）MessageType_MsgRequestVote

当Raft接收到此消息时，会在以下情况拒绝投票：

- 当Candidate的term小于当前raft的term时拒绝投票

- 如果当前raft的term与candidate的term相等，但是它之前已经投票给其他Candidate时，会拒绝投票

- 如果当前raft发现candidate的日志不如自己的日志更up-to-date时，也会拒绝投票（提前为lab2ab写）

（3）MessageType_MsgRequestVoteResponse

Candidate接收到此消息时，就会根据消息的reject属性来确定自己的得票，当自己的得票数大于一半以上，就会调用becomeLeader()函数，将状态转变为Leader；当拒绝票数也大于一半以上时，就会转回到Follower状态。



## Log replication

本部分是对raft模块日志复制功能的实现，涉及修改的代码文件主要涉及到raft.go、log.go

日志复制的流程如下：

![Log Replication](../imgs/solution/log%20replication.jpg)

本部分主要实现不同状态的raft对以下Message的处理：

- MessageType_MsgBeat
- MessageType_MsgHeartbeat
- MessageType_MsgHeartbeatResponse
- MessageType_MsgPropose
- MessageType_MsgAppend
- MessageType_MsgAppendResponse



接下来分情况实现：

（1）MessageType_MsgBeat

当上层应用调用tick()函数时，Leader需要检查是否到了该发送心跳的时候，如果到了，那么就发送MessageType_MsgHeartbeat。

leader会将自己的commit值赋给在MsgHeartbeat消息中响应值，以让Follower能够及时commit安全的entries

（2）MessageType_MsgHeartbeat

当Follower接收到心跳时，会更新自己的electionTimeout，并会将自己的lastIndex与leader的commit值比较，让自己能够及时commit entry。

（3）MessageType_MsgHeartbeatResponse

当Leader接收到心跳回复时，会比较对应Follower的Pr.Match,如果发现Follower滞后，就会向其发送缺少的entries

 (4)MessageType_MsgPropose

当Leader要添加data到自己的log entries中时，会发送一个local message—MsgPropose来让自己向所有follower同步log entries，发送MessageType_MsgAppend

（5）MessageType_MsgAppend

当Follower接收到此消息时，会在以下情况拒绝append：

- 当Leader的term小于当前raft的term时拒绝append
- 当Follower在对应Index处不含entry，说明Follower滞后比较严重
- 当Follower在对应Index处含有entry，但是term不相等，说明产生了冲突

其他情况，Follower会接收新的entries，并更新自己的相关属性。

（6）MessageType_MsgAppendResponse

当Leader发现Follower拒绝append后，会更新raft.Prs中对应Follower的进度信息，并根据新的进度，重新发送entries。



## Implement the raw node interface

本部分主要实现raw node的接口，涉及修改的代码文件为rawnode.go

RawNode对象中的属性除了Raft对象，还增加了prevSoftState和preHardState两个属性，用于在HasReady()函数中判断node是否pending

此外还实现了Advance()函数，主要是对Raft内部属性进行更新。

