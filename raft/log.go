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
	"fmt"
	pb "github.com/pingcap-incubator/tinykv/proto/pkg/eraftpb"
)

type RaftLog struct {
	// storage contains all stable entries since the last snapshot.
	storage Storage

	// unstable contains all unstable entries and snapshot.
	// they will be saved into storage.
	unstable unstable

	// committed is the highest log position that is known to be in
	// stable storage on a quorum of nodes.
	committed uint64

	// applied is the highest log position that the application has
	// been instructed to apply to its state machine.
	// Invariant: applied <= committed
	applied uint64

	logger Logger
}

// newLog returns log using the given storage. It recovers the log
// to the state that it just commits and applies the latest snapshot.
func newLog(storage Storage) *RaftLog {
	if storage == nil {
		panic("storage must not be nil")
	}
	log := &RaftLog{
		storage: storage,
		logger:  raftLogger,
	}
	firstIndex, err := storage.FirstIndex()
	if err != nil {
		panic(err)
	}
	lastIndex, err := storage.LastIndex()
	if err != nil {
		panic(err)
	}
	log.unstable.offset = lastIndex + 1
	log.unstable.logger = raftLogger
	// Initialize our committed and applied pointers to the time of the last compaction.
	log.committed = firstIndex - 1
	log.applied = firstIndex - 1
	return log
}

func (r *RaftLog) String() string {
	return fmt.Sprintf("committed=%d, applied=%d, unstable.offset=%d, len(unstable.Entries)=%d", r.committed, r.applied, r.unstable.offset, len(r.unstable.entries))
}

// maybeAppend returns (0, false) if the entries cannot be appended. Otherwise,
// it returns (last index of new entries, true).
func (r *RaftLog) maybeAppend(index, logTerm, committed uint64, ents ...pb.Entry) (lastnewi uint64, ok bool) {
	if r.matchTerm(index, logTerm) {
		lastnewi = index + uint64(len(ents))
		ci := r.findConflict(ents)
		switch {
		case ci == 0:
		case ci <= r.committed:
			r.logger.Panicf("entry %d conflict with committed entry [committed(%d)]", ci, r.committed)
		default:
			offset := index + 1
			r.append(ents[ci-offset:]...)
		}
		r.commitTo(min(committed, lastnewi))
		return lastnewi, true
	}
	return 0, false
}

func (r *RaftLog) append(ents ...pb.Entry) uint64 {
	if len(ents) == 0 {
		return r.LastIndex()
	}
	if after := ents[0].Index - 1; after < r.committed {
		r.logger.Panicf("after(%d) is out of range [committed(%d)]", after, r.committed)
	}
	r.unstable.truncateAndAppend(ents)
	return r.LastIndex()
}

// findConflict finds the index of the conflict.
// It returns the first pair of conflicting entries between the existing
// entries and the given entries, if there are any.
// If there is no conflicting entries, and the existing entries contains
// all the given entries, zero will be returned.
// If there is no conflicting entries, but the given entries contains new
// entries, the index of the first new entry will be returned.
// An entry is considered to be conflicting if it has the same index but
// a different term.
// The first entry MUST have an index equal to the argument 'from'.
// The index of the given entries MUST be continuously increasing.
func (r *RaftLog) findConflict(ents []pb.Entry) uint64 {
	for _, ne := range ents {
		if !r.matchTerm(ne.Index, ne.Term) {
			if ne.Index <= r.LastIndex() {
				r.logger.Infof("found conflict at index %d [existing term: %d, conflicting term: %d]",
					ne.Index, r.zeroTermOnErrCompacted(r.Term(ne.Index)), ne.Term)
			}
			return ne.Index
		}
	}
	return 0
}

func (r *RaftLog) unstableEntries() []pb.Entry {
	if len(r.unstable.entries) == 0 {
		return []pb.Entry{}
	}
	return r.unstable.entries
}

// nextEnts returns all the available entries for execution.
// If applied is smaller than the index of snapshot, it returns all committed
// entries after the index of snapshot.
func (r *RaftLog) nextEnts() (ents []pb.Entry) {
	off := max(r.applied+1, r.firstIndex())
	if r.committed+1 > off {
		ents, err := r.slice(off, r.committed+1)
		if err != nil {
			r.logger.Panicf("unexpected error when getting unapplied entries (%v)", err)
		}
		return ents
	}
	return nil
}

// hasNextEnts returns if there is any available entries for execution. This
// is a fast check without heavy raftLog.slice() in raftLog.nextEnts().
func (r *RaftLog) hasNextEnts() bool {
	off := max(r.applied+1, r.firstIndex())
	return r.committed+1 > off
}

// hasPendingSnapshot returns if there is pending snapshot waiting for applying.
func (r *RaftLog) hasPendingSnapshot() bool {
	return r.unstable.snapshot != nil && !IsEmptySnap(r.unstable.snapshot)
}

func (r *RaftLog) snapshot() (pb.Snapshot, error) {
	if r.unstable.snapshot != nil {
		return *r.unstable.snapshot, nil
	}
	return r.storage.Snapshot()
}

func (r *RaftLog) firstIndex() uint64 {
	if i, ok := r.unstable.maybeFirstIndex(); ok {
		return i
	}
	index, err := r.storage.FirstIndex()
	if err != nil {
		panic(err) // TODO(bdarnell)
	}
	return index
}

func (r *RaftLog) LastIndex() uint64 {
	if i, ok := r.unstable.maybeLastIndex(); ok {
		return i
	}
	i, err := r.storage.LastIndex()
	if err != nil {
		panic(err) // TODO(bdarnell)
	}
	return i
}

func (r *RaftLog) commitTo(tocommit uint64) {
	// never decrease commit
	if r.committed < tocommit {
		if r.LastIndex() < tocommit {
			r.logger.Panicf("tocommit(%d) is out of range [lastIndex(%d)]. Was the raft log corrupted, truncated, or lost?", tocommit, r.LastIndex())
		}
		r.committed = tocommit
	}
}

func (r *RaftLog) appliedTo(i uint64) {
	if i == 0 {
		return
	}
	if r.committed < i || i < r.applied {
		r.logger.Panicf("applied(%d) is out of range [prevApplied(%d), committed(%d)]", i, r.applied, r.committed)
	}
	r.applied = i
}

func (r *RaftLog) stableTo(i, t uint64) { r.unstable.stableTo(i, t) }

func (r *RaftLog) stableSnapTo(i uint64) { r.unstable.stableSnapTo(i) }

func (r *RaftLog) lastTerm() uint64 {
	t, err := r.Term(r.LastIndex())
	if err != nil {
		r.logger.Panicf("unexpected error when getting the last term (%v)", err)
	}
	return t
}

func (r *RaftLog) Term(i uint64) (uint64, error) {
	// the valid term range is [index of dummy entry, last index]
	dummyIndex := r.firstIndex() - 1
	if i < dummyIndex || i > r.LastIndex() {
		return 0, nil
	}

	if t, ok := r.unstable.maybeTerm(i); ok {
		return t, nil
	}

	t, err := r.storage.Term(i)
	if err == nil {
		return t, nil
	}
	if err == ErrCompacted || err == ErrUnavailable {
		return 0, err
	}
	panic(err)
}

func (r *RaftLog) entries(i uint64) ([]pb.Entry, error) {
	if i > r.LastIndex() {
		return nil, nil
	}
	return r.slice(i, r.LastIndex()+1)
}

// allEntries returns all entries in the log.
func (r *RaftLog) allEntries() []pb.Entry {
	ents, err := r.entries(r.firstIndex())
	if err == nil {
		return ents
	}
	if err == ErrCompacted { // try again if there was a racing compaction
		return r.allEntries()
	}
	panic(err)
}

// isUpToDate determines if the given (lastIndex,term) log is more up-to-date
// by comparing the index and term of the last entries in the existing logs.
// If the logs have last entries with different terms, then the log with the
// later term is more up-to-date. If the logs end with the same term, then
// whichever log has the larger lastIndex is more up-to-date. If the logs are
// the same, the given log is up-to-date.
func (r *RaftLog) isUpToDate(lasti, term uint64) bool {
	return term > r.lastTerm() || (term == r.lastTerm() && lasti >= r.LastIndex())
}

func (r *RaftLog) matchTerm(i, term uint64) bool {
	t, err := r.Term(i)
	if err != nil {
		return false
	}
	return t == term
}

func (r *RaftLog) maybeCommit(maxIndex, term uint64) bool {
	if maxIndex > r.committed && r.zeroTermOnErrCompacted(r.Term(maxIndex)) == term {
		r.commitTo(maxIndex)
		return true
	}
	return false
}

func (r *RaftLog) restore(s *pb.Snapshot) {
	r.logger.Infof("log [%s] starts to restore snapshot [index: %d, term: %d]", r, s.Metadata.Index, s.Metadata.Term)
	r.committed = s.Metadata.Index
	r.unstable.restore(s)
}

// slice returns a slice of log entries from lo through hi-1, inclusive.
func (r *RaftLog) slice(lo, hi uint64) ([]pb.Entry, error) {
	err := r.mustCheckOutOfBounds(lo, hi)
	if err != nil {
		return nil, err
	}
	if lo == hi {
		return nil, nil
	}
	var ents []pb.Entry
	if lo < r.unstable.offset {
		storedEnts, err := r.storage.Entries(lo, min(hi, r.unstable.offset))
		if err == ErrCompacted {
			return nil, err
		} else if err == ErrUnavailable {
			r.logger.Panicf("entries[%d:%d) is unavailable from storage", lo, min(hi, r.unstable.offset))
		} else if err != nil {
			panic(err)
		}

		ents = storedEnts
	}
	if hi > r.unstable.offset {
		unstable := r.unstable.slice(max(lo, r.unstable.offset), hi)
		if len(ents) > 0 {
			combined := make([]pb.Entry, len(ents)+len(unstable))
			n := copy(combined, ents)
			copy(combined[n:], unstable)
			ents = combined
		} else {
			ents = unstable
		}
	}
	return ents, nil
}

// r.firstIndex <= lo <= hi <= r.firstIndex + len(r.entries)
func (r *RaftLog) mustCheckOutOfBounds(lo, hi uint64) error {
	if lo > hi {
		r.logger.Panicf("invalid slice %d > %d", lo, hi)
	}
	fi := r.firstIndex()
	if lo < fi {
		return ErrCompacted
	}

	length := r.LastIndex() + 1 - fi
	if hi > fi+length {
		r.logger.Panicf("slice[%d,%d) out of bound [%d,%d]", lo, hi, fi, r.LastIndex())
	}
	return nil
}

func (r *RaftLog) zeroTermOnErrCompacted(t uint64, err error) uint64 {
	if err == nil {
		return t
	}
	if err == ErrCompacted {
		return 0
	}
	r.logger.Panicf("unexpected error (%v)", err)
	return 0
}
