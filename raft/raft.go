// Copyright 2015 The etcd Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package raft

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	pb "github.com/pingcap-incubator/tinykv/proto/pkg/eraftpb"
)

// None is a placeholder node ID used when there is no leader.
const None uint64 = 0

// StateType represents the role of a node in a cluster.
type StateType uint64

const (
	StateFollower StateType = iota
	StateCandidate
	StateLeader
)

var stmap = [...]string{
	"StateFollower",
	"StateCandidate",
	"StateLeader",
}

func (st StateType) String() string {
	return stmap[uint64(st)]
}

// lockedRand is a small wrapper around rand.Rand to provide
// synchronization among multiple raft groups. Only the methods needed
// by the code are exposed (e.g. Intn).
type lockedRand struct {
	mu   sync.Mutex
	rand *rand.Rand
}

func (r *lockedRand) Intn(n int) int {
	r.mu.Lock()
	v := r.rand.Intn(n)
	r.mu.Unlock()
	return v
}

var globalRand = &lockedRand{
	rand: rand.New(rand.NewSource(time.Now().UnixNano())),
}

// ErrProposalDropped is returned when the proposal is ignored by some cases,
// so that the proposer can be notified and fail fast.
var ErrProposalDropped = errors.New("raft proposal dropped")

// Config contains the parameters to start a raft.
type Config struct {
	// ID is the identity of the local raft. ID cannot be 0.
	ID uint64

	// peers contains the IDs of all nodes (including self) in the raft cluster. It
	// should only be set when starting a new raft cluster. Restarting raft from
	// previous configuration will panic if peers is set. peer is private and only
	// used for testing right now.
	peers []uint64

	// ElectionTick is the number of Node.Tick invocations that must pass between
	// elections. That is, if a follower does not receive any message from the
	// leader of current term before ElectionTick has elapsed, it will become
	// candidate and start an election. ElectionTick must be greater than
	// HeartbeatTick. We suggest ElectionTick = 10 * HeartbeatTick to avoid
	// unnecessary leader switching.
	ElectionTick int
	// HeartbeatTick is the number of Node.Tick invocations that must pass between
	// heartbeats. That is, a leader sends heartbeat messages to maintain its
	// leadership every HeartbeatTick ticks.
	HeartbeatTick int

	// Storage is the storage for raft. raft generates entries and states to be
	// stored in storage. raft reads the persisted entries and states out of
	// Storage when it needs. raft reads out the previous state and configuration
	// out of storage when restarting.
	Storage Storage
	// Applied is the last applied index. It should only be set when restarting
	// raft. raft will not return entries to the application smaller or equal to
	// Applied. If Applied is unset when restarting, raft might return previous
	// applied entries. This is a very application dependent configuration.
	Applied uint64

	// Logger is the logger used for raft log. For multinode which can host
	// multiple raft group, each raft group can have its own logger
	Logger Logger
}

func (c *Config) validate() error {
	if c.ID == None {
		return errors.New("cannot use none as id")
	}

	if c.HeartbeatTick <= 0 {
		return errors.New("heartbeat tick must be greater than 0")
	}

	if c.ElectionTick <= c.HeartbeatTick {
		return errors.New("election tick must be greater than heartbeat tick")
	}

	if c.Storage == nil {
		return errors.New("storage cannot be nil")
	}

	if c.Logger == nil {
		c.Logger = raftLogger
	}

	return nil
}

// Progress represents a follower’s progress in the view of the leader. Leader maintains
// progresses of all followers, and sends entries to the follower based on its progress.
type Progress struct {
	Match, Next uint64
}

func (p *Progress) String() string {
	return fmt.Sprintf("{MatchIndex:%d,NextIndex:%d}", p.Match, p.Next)
}

type Raft struct {
	id uint64

	Term uint64
	Vote uint64

	// the log
	RaftLog *RaftLog

	// log replication progress of each peers
	Prs *ProgressTracker

	// this peer's role
	State StateType

	// msgs need to send
	msgs []pb.Message

	// the leader id
	Lead uint64

	// heartbeat interval, should send
	heartbeatTimeout int
	// baseline of election interval
	electionTimeout int
	// random election interval between [electionTimeout, 2 * electionTimeout]
	randomizedElectionTimeout int
	// number of ticks since it reached last heartbeatTimeout.
	// only leader keeps heartbeatElapsed.
	heartbeatElapsed int
	// Ticks since it reached last electionTimeout when it is leader or candidate.
	// Number of ticks since it reached last electionTimeout or received a
	// valid message from current leader when it is a follower.
	electionElapsed int

	// leadTransferee is id of the leader transfer target when its value is not zero.
	// Follow the procedure defined in section 3.10 of Raft phd thesis.
	// (https://web.stanford.edu/~ouster/cgi-bin/papers/OngaroPhD.pdf)
	// (Used in 3A leader transfer)
	leadTransferee uint64

	// Only one conf change may be pending (in the log, but not yet
	// applied) at a time. This is enforced via PendingConfIndex, which
	// is set to a value >= the log index of the latest pending
	// configuration change (if any). Config changes are only allowed to
	// be proposed if the leader's applied index is greater than this
	// value.
	// (Used in 3A conf change)
	PendingConfIndex uint64

	logger Logger
}

// newRaft return a raft peer with the given config
func newRaft(c *Config) *Raft {
	if err := c.validate(); err != nil {
		panic(err.Error())
	}

	raftLog := newLog(c.Storage)
	hardState, confState, err := c.Storage.InitialState()
	if err != nil {
		panic(err)
	}
	r := &Raft{
		id:               c.ID,
		RaftLog:          raftLog,
		Prs:              MakeProgressTracker(),
		electionTimeout:  c.ElectionTick,
		heartbeatTimeout: c.HeartbeatTick,
		logger:           c.Logger,
	}

	if len(c.peers) != 0 {
		// lab2a
		for _, id := range c.peers {
			r.Prs.Progress[id] = &Progress{
				Next: raftLog.LastIndex(),
			}
		}
	} else {
		// lab2b
		for _, id := range confState.Nodes {
			r.Prs.Progress[id] = &Progress{
				Next: raftLog.LastIndex(),
			}
		}
	}

	if !IsEmptyHardState(hardState) {
		r.loadState(hardState)
	}
	if c.Applied > 0 {
		raftLog.appliedTo(c.Applied)
	}
	r.becomeFollower(r.Term, None)
	return r
}

// sendAppend sends an append RPC with new entries (if any) and the
// current commit index to the given peer. Returns true if a message was sent.
func (r *Raft) sendAppend(to uint64) bool {
	return r.maybeSendAppend(to, true)
}

// maybeSendAppend sends an append RPC with new entries to the given peer,
// if necessary. Returns true if a message was sent. The sendIfEmpty
// argument controls whether messages with no entries will be sent
// ("empty" messages are useful to convey updated Commit indexes, but
// are undesirable when we're sending multiple messages in a batch).
func (r *Raft) maybeSendAppend(to uint64, sendIfEmpty bool) bool {
	pr := r.Prs.Progress[to]

	m := pb.Message{}
	m.To = to

	term, errt := r.RaftLog.Term(pr.Next - 1)
	ents, erre := r.RaftLog.entries(pr.Next)
	if len(ents) == 0 && !sendIfEmpty {
		return false
	}

	if errt != nil || erre != nil { // send snapshot if we failed to get term or entries
		m.MsgType = pb.MessageType_MsgSnapshot
		snapshot, err := r.RaftLog.snapshot()
		if err != nil {
			if err == ErrSnapshotTemporarilyUnavailable {
				r.logger.Debugf("%x failed to send snapshot to %x because snapshot is temporarily unavailable", r.id, to)
				return false
			}
			panic(err)
		}
		if IsEmptySnap(&snapshot) {
			panic("need non-empty snapshot")
		}
		m.Snapshot = &snapshot
		sindex, sterm := snapshot.Metadata.Index, snapshot.Metadata.Term
		r.logger.Debugf("%x [firstindex: %d, commit: %d] sent snapshot[index: %d, term: %d] to %x [%s]",
			r.id, r.RaftLog.firstIndex(), r.RaftLog.committed, sindex, sterm, to, pr)
	} else {
		m.MsgType = pb.MessageType_MsgAppend
		m.Index = pr.Next - 1
		m.LogTerm = term
		m.Entries = transformToPointers(ents)
		m.Commit = r.RaftLog.committed
	}
	r.send(m)
	return true
}

// sendHeartbeat sends a heartbeat RPC to the given peer.
func (r *Raft) sendHeartbeat(to uint64) {
	// Attach the commit as min(to.matched, r.committed).
	// When the leader sends out heartbeat message,
	// the receiver(follower) might not be matched with the leader
	// or it might not have all the committed entries.
	// The leader MUST NOT forward the follower's commit to
	// an unmatched index.
	commit := min(r.Prs.Progress[to].Match, r.RaftLog.committed)
	m := pb.Message{
		To:      to,
		MsgType: pb.MessageType_MsgHeartbeat,
		Commit:  commit,
	}

	r.send(m)
}

// tick advances the internal logical clock by a single tick.
func (r *Raft) tick() {
	switch r.State {
	case StateFollower, StateCandidate:
		r.electionElapsed++
		if r.pastRandomizedElectionTimeout() {
			r.electionElapsed = 0
			r.Step(pb.Message{From: r.id, MsgType: pb.MessageType_MsgHup})
		}
	case StateLeader:
		r.heartbeatElapsed++
		r.electionElapsed++
		if r.pastElectionTimeout() {
			r.electionElapsed = 0
		}
		if r.pastHeartbeatTimeout() {
			r.heartbeatElapsed = 0
			r.Step(pb.Message{From: r.id, MsgType: pb.MessageType_MsgBeat})
		}
	}
}

// becomeFollower transform this peer's state to Follower
func (r *Raft) becomeFollower(term uint64, lead uint64) {
	r.reset(term)
	r.Lead = lead
	r.State = StateFollower
	r.logger.Infof("%x become follower at term %d with Lead=%d，lastTerm=%d, lastIndex=%d, commitIndex=%d, applyIndex=%d",
		r.id, r.Term, r.Lead, r.RaftLog.lastTerm(), r.RaftLog.LastIndex(), r.RaftLog.committed, r.RaftLog.applied)
}

// becomeCandidate transform this peer's state to candidate
func (r *Raft) becomeCandidate() {
	r.reset(r.Term + 1)
	r.Vote = r.id
	r.State = StateCandidate
	r.logger.Infof("%x become candidate at term %d with lastTerm=%d, lastIndex=%d, commitIndex=%d, applyIndex=%d",
		r.id, r.Term, r.RaftLog.lastTerm(), r.RaftLog.LastIndex(), r.RaftLog.committed, r.RaftLog.applied)
}

// becomeLeader transform this peer's state to leader
func (r *Raft) becomeLeader() {
	r.reset(r.Term)
	r.Lead = r.id
	r.State = StateLeader
	r.logger.Infof("%x become leader at term %d with lastTerm=%d, lastIndex=%d, commitIndex=%d, applyIndex=%d",
		r.id, r.Term, r.RaftLog.lastTerm(), r.RaftLog.LastIndex(), r.RaftLog.committed, r.RaftLog.applied)
	emptyEnt := pb.Entry{Data: nil}
	if r.appendEntry([]*pb.Entry{&emptyEnt}...) {
		r.logger.Infof("%x append a empty entry at term %d with index=%d",
			r.id, r.Term, r.RaftLog.LastIndex())
	} else {
		// This won't happen because we just called reset() above.
		r.logger.Panic("empty entry was dropped")
	}
}

func (r *Raft) stepFollower(m pb.Message) error {
	switch m.MsgType {
	case pb.MessageType_MsgPropose:
		if r.Lead == None {
			return ErrProposalDropped
		}
		m.To = r.Lead
		r.send(m)
	case pb.MessageType_MsgAppend:
		r.electionElapsed = 0
		r.Lead = m.From
		r.handleAppendEntries(m)
	case pb.MessageType_MsgHeartbeat:
		r.electionElapsed = 0
		r.Lead = m.From
		r.handleHeartbeat(m)
	case pb.MessageType_MsgSnapshot:
		r.electionElapsed = 0
		r.Lead = m.From
		r.handleSnapshot(m)
	}
	return nil
}

func (r *Raft) stepCandidate(m pb.Message) error {
	switch m.MsgType {
	case pb.MessageType_MsgPropose:
		r.logger.Infof("%x no leader at term %d; dropping proposal", r.id, r.Term)
		return ErrProposalDropped
	case pb.MessageType_MsgAppend:
		r.becomeFollower(m.Term, m.From)
		r.handleAppendEntries(m)
	case pb.MessageType_MsgHeartbeatResponse:
		r.becomeFollower(m.Term, m.From)
		r.handleHeartbeat(m)
	case pb.MessageType_MsgSnapshot:
		r.becomeFollower(m.Term, m.From)
		r.handleSnapshot(m)
	case pb.MessageType_MsgRequestVoteResponse:
		res := r.poll(m.From, !m.Reject)
		switch res {
		case VoteWon:
			r.becomeLeader()
			r.bcastAppend()
		case VoteLost:
			r.becomeFollower(r.Term, None)
		}
	}
	return nil
}

func (r *Raft) stepLeader(m pb.Message) error {
	// These message types do not require any progress for m.From.
	switch m.MsgType {
	case pb.MessageType_MsgBeat:
		r.bcastHeartbeat()
		return nil
	case pb.MessageType_MsgPropose:
		if len(m.Entries) == 0 {
			r.logger.Panicf("%x stepped empty MsgProp", r.id)
		}
		if r.Prs.Progress[r.id] == nil {
			// If we are not currently a member of the range (i.e. this node
			// was removed from the configuration while serving as leader),
			// drop any new proposals.
			return ErrProposalDropped
		}
		if r.leadTransferee != None {
			r.logger.Debugf("%x [term %d] transfer leadership to %x is in progress; dropping proposal", r.id, r.Term, r.leadTransferee)
			return ErrProposalDropped
		}

		if !r.appendEntry(m.Entries...) {
			return ErrProposalDropped
		}
		r.bcastAppend()
		return nil
	}

	// All other message types require a progress for m.From (pr).
	pr := r.Prs.Progress[m.From]
	if pr == nil {
		r.logger.Debugf("%x no progress available for %x", r.id, m.From)
		return nil
	}
	switch m.MsgType {
	case pb.MessageType_MsgAppendResponse:
		if m.Reject {
			r.logger.Debugf("%x received MsgAppResp(MsgApp was rejected, lastindex: %d) from %x for index %d",
				r.id, m.From, m.Index)
			if pr.MaybeDecrTo(m.Index) {
				r.logger.Debugf("%x decreased progress of %x to [%s]", r.id, m.From, pr)
				r.sendAppend(m.From)
			}
		} else {
			if pr.MaybeUpdate(m.Index) {
				if r.maybeCommit() {
					r.bcastAppend()
				}
				r.maybeSendAppend(m.From, false)
			}
		}
	case pb.MessageType_MsgHeartbeatResponse:
		if pr.Match < r.RaftLog.LastIndex() {
			r.sendAppend(m.From)
		}
	}
	return nil
}

// Step the entrance of handle message, see `MessageType`
// on `eraftpb.proto` for what msgs should be handled
func (r *Raft) Step(m pb.Message) error {
	// Handle the message term, which may result in our stepping down to a follower.
	switch {
	case m.Term == 0:
		// local message
	case m.Term > r.Term:
		if m.MsgType == pb.MessageType_MsgAppend || m.MsgType == pb.MessageType_MsgHeartbeat || m.MsgType == pb.MessageType_MsgSnapshot {
			r.becomeFollower(m.Term, m.From)
		} else {
			r.becomeFollower(m.Term, None)
		}
	case m.Term < r.Term:
		if m.MsgType == pb.MessageType_MsgHeartbeat || m.MsgType == pb.MessageType_MsgAppend {
			r.send(pb.Message{To: m.From, MsgType: pb.MessageType_MsgAppendResponse})
		}
		return nil
	}

	switch m.MsgType {
	case pb.MessageType_MsgHup:
		r.hup()
	case pb.MessageType_MsgRequestVote:
		// We can vote if this is a repeat of a vote we've already cast...
		canVote := r.Vote == m.From ||
			// ...we haven't voted and we don't think there's a leader yet in this term...
			(r.Vote == None && r.Lead == None)
		// ...and we believe the candidate is up to date.
		if canVote && r.RaftLog.isUpToDate(m.Index, m.LogTerm) {
			r.electionElapsed = 0
			r.Vote = m.From
			// same in the case of regular votes, but different for pre-votes.
			r.send(pb.Message{To: m.From, Term: m.Term, MsgType: pb.MessageType_MsgRequestVoteResponse})
		} else {
			r.send(pb.Message{To: m.From, Term: r.Term, MsgType: pb.MessageType_MsgRequestVoteResponse, Reject: true})
		}
	default:
		switch r.State {
		case StateLeader:
			err := r.stepLeader(m)
			if err != nil {
				return err
			}
		case StateFollower:
			err := r.stepFollower(m)
			if err != nil {
				return err
			}
		case StateCandidate:
			err := r.stepCandidate(m)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// send schedules persisting state to a stable storage and AFTER that
// sending the message (as part of next Ready message processing).
func (r *Raft) send(m pb.Message) {
	if m.From == None {
		m.From = r.id
	}
	if m.MsgType == pb.MessageType_MsgRequestVote || m.MsgType == pb.MessageType_MsgRequestVoteResponse {
		if m.Term == 0 {
			// All {pre-,}campaign messages need to have the term set when
			// sending.
			// - MsgVote: m.Term is the term the node is campaigning for,
			//   non-zero as we increment the term when campaigning.
			// - MsgVoteResp: m.Term is the new r.Term if the MsgVote was
			//   granted, non-zero for the same reason MsgVote is
			panic(fmt.Sprintf("term should be set when sending %s", m.MsgType))
		}
	} else {
		if m.Term != 0 {
			panic(fmt.Sprintf("term should not be set when sending %s (was %d)", m.MsgType, m.Term))
		}
		// do not attach term to MsgProp
		// proposals are a way to forward to the leader and
		// should be treated as local message.
		if m.MsgType != pb.MessageType_MsgPropose {
			m.Term = r.Term
		}
	}
	r.msgs = append(r.msgs, m)
}

// handleAppendEntries handle AppendEntries RPC request
func (r *Raft) handleAppendEntries(m pb.Message) {
	if m.Index < r.RaftLog.committed {
		r.send(pb.Message{To: m.From, MsgType: pb.MessageType_MsgAppendResponse, Index: r.RaftLog.committed})
		return
	}

	if mlastIndex, ok := r.RaftLog.maybeAppend(m.Index, m.LogTerm, m.Commit, transformFromPointers(m.Entries)...); ok {
		r.send(pb.Message{To: m.From, MsgType: pb.MessageType_MsgAppendResponse, Index: mlastIndex})
	} else {
		r.logger.Debugf("%x [logterm: %d, index: %d] rejected MsgApp [logterm: %d, index: %d] from %x",
			r.id, r.RaftLog.zeroTermOnErrCompacted(r.RaftLog.Term(m.Index)), m.Index, m.LogTerm, m.Index, m.From)
		r.send(pb.Message{To: m.From, MsgType: pb.MessageType_MsgAppendResponse, Index: m.Index, Reject: true})
	}
}

// handleHeartbeat handle Heartbeat RPC request
func (r *Raft) handleHeartbeat(m pb.Message) {
	r.RaftLog.commitTo(m.Commit)
	r.send(pb.Message{To: m.From, MsgType: pb.MessageType_MsgHeartbeatResponse})
}

// handleSnapshot handle Snapshot RPC request
func (r *Raft) handleSnapshot(m pb.Message) {
	sindex, sterm := m.Snapshot.Metadata.Index, m.Snapshot.Metadata.Term
	if r.restore(*m.Snapshot) {
		r.logger.Infof("%x [commit: %d] restored snapshot [index: %d, term: %d]",
			r.id, r.RaftLog.committed, sindex, sterm)
		r.send(pb.Message{To: m.From, MsgType: pb.MessageType_MsgAppendResponse, Index: r.RaftLog.LastIndex()})
	} else {
		r.logger.Infof("%x [commit: %d] ignored snapshot [index: %d, term: %d]",
			r.id, r.RaftLog.committed, sindex, sterm)
		r.send(pb.Message{To: m.From, MsgType: pb.MessageType_MsgAppendResponse, Index: r.RaftLog.committed})
	}
}

// restore recovers the state machine from a snapshot. It restores the log and the
// configuration of state machine. If this method returns false, the snapshot was
// ignored, either because it was obsolete or because of an error.
func (r *Raft) restore(s pb.Snapshot) bool {
	if s.Metadata.Index <= r.RaftLog.committed {
		return false
	}
	if r.State != StateFollower {
		// This is defense-in-depth: if the leader somehow ended up applying a
		// snapshot, it could move into a new term without moving into a
		// follower state. This should never fire, but if it did, we'd have
		// prevented damage by returning early, so log only a loud warning.
		//
		// At the time of writing, the instance is guaranteed to be in follower
		// state when this method is called.
		r.logger.Warningf("%x attempted to restore snapshot as leader; should never happen", r.id)
		r.becomeFollower(r.Term+1, None)
		return false
	}

	// More defense-in-depth: throw away snapshot if recipient is not in the
	// config. This shouldn't ever happen (at the time of writing) but lots of
	// code here and there assumes that r.id is in the progress tracker.
	found := false
	cs := s.Metadata.ConfState

	for _, id := range cs.Nodes {
		if id == r.id {
			found = true
			break
		}
	}
	if !found {
		r.logger.Warningf(
			"%x attempted to restore snapshot but it is not in the ConfState %v; should never happen",
			r.id, cs,
		)
		return false
	}

	// Now go ahead and actually restore.
	if r.RaftLog.matchTerm(s.Metadata.Index, s.Metadata.Term) {
		r.logger.Infof("%x [commit: %d, lastindex: %d, lastterm: %d] fast-forwarded commit to snapshot [index: %d, term: %d]",
			r.id, r.RaftLog.committed, r.RaftLog.LastIndex(), r.RaftLog.lastTerm(), s.Metadata.Index, s.Metadata.Term)
		r.RaftLog.commitTo(s.Metadata.Index)
		return false
	}

	r.RaftLog.restore(&s)

	// Reset the configuration and add the (potentially updated) peers in anew.
	r.Prs = MakeProgressTracker()

	for _, id := range cs.Nodes {
		_, ok := r.Prs.Progress[id]
		if !ok {
			r.Prs.Progress[id] = &Progress{
				Next: r.RaftLog.LastIndex(),
			}
		} else {
			r.Prs.Progress[id] = &Progress{
				Next:  r.Prs.Progress[id].Next,
				Match: r.Prs.Progress[id].Match,
			}
		}
	}

	r.logger.Infof("%x [commit: %d, lastindex: %d, lastterm: %d] restored snapshot [index: %d, term: %d]",
		r.id, r.RaftLog.committed, r.RaftLog.LastIndex(), r.RaftLog.lastTerm(), s.Metadata.Index, s.Metadata.Term)
	return true
}

// addNode add a new node to raft group
func (r *Raft) addNode(id uint64) {
	// Your Code Here (3A).
}

// removeNode remove a node from raft group
func (r *Raft) removeNode(id uint64) {
	// Your Code Here (3A).
}

func (r *Raft) pastElectionTimeout() bool {
	return r.electionElapsed >= r.electionTimeout
}

func (r *Raft) pastRandomizedElectionTimeout() bool {
	return r.electionElapsed >= r.randomizedElectionTimeout
}

func (r *Raft) pastHeartbeatTimeout() bool {
	return r.heartbeatElapsed >= r.heartbeatTimeout
}

func (r *Raft) resetRandomizedElectionTimeout() {
	r.randomizedElectionTimeout = r.electionTimeout + globalRand.Intn(r.electionTimeout)
}

func (r *Raft) reset(term uint64) {
	if r.Term != term {
		r.Term = term
		r.Vote = None
	}
	r.Lead = None

	r.electionElapsed = 0
	r.heartbeatElapsed = 0
	r.resetRandomizedElectionTimeout()
	r.Prs.ResetVotes()
	r.Prs.Visit(func(id uint64, pr *Progress) {
		*pr = Progress{
			Match: 0,
			Next:  r.RaftLog.LastIndex() + 1,
		}
		if id == r.id {
			pr.Match = r.RaftLog.LastIndex()
		}
	})
}

func (r *Raft) loadState(state pb.HardState) {
	r.RaftLog.committed = state.Commit
	r.Term = state.Term
	r.Vote = state.Vote
}

func (r *Raft) poll(id uint64, v bool) (result VoteResult) {
	r.Prs.RecordVote(id, v)
	return r.Prs.TallyVotes()
}

// bcastAppend sends RPC, with entries to all peers that are not up-to-date
// according to the progress recorded in r.prs.
func (r *Raft) bcastAppend() {
	r.Prs.Visit(func(id uint64, _ *Progress) {
		if id == r.id {
			return
		}
		r.sendAppend(id)
	})
}

func (r *Raft) bcastHeartbeat() {
	r.Prs.Visit(func(id uint64, _ *Progress) {
		if id == r.id {
			return
		}
		r.sendHeartbeat(id)
	})
}

func (r *Raft) appendEntry(es ...*pb.Entry) (accepted bool) {
	li := r.RaftLog.LastIndex()
	entries := make([]pb.Entry, 0, len(es))
	for i := range es {
		es[i].Term = r.Term
		es[i].Index = li + 1 + uint64(i)
		entries = append(entries, *es[i])
	}

	// use latest "last" index after truncate/append
	li = r.RaftLog.append(entries...)
	if r.Prs.Progress[r.id].MaybeUpdate(li) {
		// Regardless of maybeCommit's return, our caller will call bcastAppend.
		r.maybeCommit()
	}
	return true
}

func (r *Raft) hup() {
	if r.State == StateLeader {
		r.logger.Debugf("%x ignoring MsgHup because already leader", r.id)
		return
	}
	r.logger.Infof("%x is starting a new election at term %d", r.id, r.Term)
	r.becomeCandidate()
	if res := r.poll(r.id, true); res == VoteWon {
		// We won the election after voting for ourselves (which must mean that
		// this is a single-node cluster). Advance to the next state.
		r.becomeLeader()
		return
	}
	for id := range r.Prs.Progress {
		if id == r.id {
			continue
		}
		r.logger.Infof("%x [logterm: %d, index: %d] sent MessageType_MsgRequestVote request to %x at term %d",
			r.id, r.RaftLog.lastTerm(), r.RaftLog.LastIndex(), id, r.Term)
		r.send(pb.Message{Term: r.Term, To: id, MsgType: pb.MessageType_MsgRequestVote, Index: r.RaftLog.LastIndex(), LogTerm: r.RaftLog.lastTerm()})
	}
}

// maybeCommit attempts to advance the commit index. Returns true if
// the commit index changed (in which case the caller should call
// r.bcastAppend).
func (r *Raft) maybeCommit() bool {
	mci := r.Prs.Committed()
	return r.RaftLog.maybeCommit(mci, r.Term)
}

func (r *Raft) softState() *SoftState { return &SoftState{Lead: r.Lead, RaftState: r.State} }

func (r *Raft) hardState() pb.HardState {
	return pb.HardState{
		Term:   r.Term,
		Vote:   r.Vote,
		Commit: r.RaftLog.committed,
	}
}

func (r *Raft) advance(rd Ready) {
	// If entries were applied (or a snapshot), update our cursor for
	// the next Ready. Note that if the current HardState contains a
	// new Commit index, this does not mean that we're also applying
	// all of the new entries due to commit pagination by size.
	if newApplied := rd.appliedCursor(); newApplied > 0 {
		r.RaftLog.appliedTo(newApplied)
	}

	if len(rd.Entries) > 0 {
		e := rd.Entries[len(rd.Entries)-1]
		r.RaftLog.stableTo(e.Index, e.Term)
	}
	if !IsEmptySnap(&rd.Snapshot) {
		r.RaftLog.stableSnapTo(rd.Snapshot.Metadata.Index)
	}
}
