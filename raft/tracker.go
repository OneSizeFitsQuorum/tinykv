package raft

type ProgressTracker struct {
	Progress map[uint64]*Progress

	Votes map[uint64]bool
}

// MakeProgressTracker initializes a ProgressTracker.
func MakeProgressTracker() *ProgressTracker {
	p := ProgressTracker{
		Votes:    map[uint64]bool{},
		Progress: map[uint64]*Progress{},
	}
	return &p
}

// ResetVotes prepares for a new round of vote counting via recordVote.
func (p *ProgressTracker) ResetVotes() {
	p.Votes = map[uint64]bool{}
}

// RecordVote records that the node with the given id voted for this Raft
// instance if v == true (and declined it otherwise).
func (p *ProgressTracker) RecordVote(id uint64, v bool) {
	_, ok := p.Votes[id]
	if !ok {
		p.Votes[id] = v
	}
}

func (p *ProgressTracker) TallyVotes() (result VoteResult) {
	missing := 0
	granted := 0
	rejected := 0

	for id := range p.Progress {
		v, voted := p.Votes[id]
		if !voted {
			missing++
			continue
		}
		if v {
			granted++
		} else {
			rejected++
		}
	}
	q := len(p.Progress)/2 + 1
	if granted >= q {
		return VoteWon
	}
	if granted+missing >= q {
		return VotePending
	}
	return VoteLost
}

func insertionSort(sl []uint64) {
	a, b := 0, len(sl)
	for i := a + 1; i < b; i++ {
		for j := i; j > a && sl[j] < sl[j-1]; j-- {
			sl[j], sl[j-1] = sl[j-1], sl[j]
		}
	}
}

// Visit invokes the supplied closure for all tracked progresses in stable order.
func (p *ProgressTracker) Visit(f func(id uint64, pr *Progress)) {
	n := len(p.Progress)
	// We need to sort the IDs and don't want to allocate since this is hot code.
	var sl [7]uint64
	ids := sl[:]
	if len(sl) >= n {
		ids = sl[:n]
	} else {
		ids = make([]uint64, n)
	}
	for id := range p.Progress {
		n--
		ids[n] = id
	}
	insertionSort(ids)
	for _, id := range ids {
		f(id, p.Progress[id])
	}
}

func (p *ProgressTracker) Committed() uint64 {
	n := len(p.Progress)
	// We need to sort the IDs and don't want to allocate since this is hot code.
	var sl [7]uint64
	srt := sl[:]
	if len(sl) >= n {
		srt = sl[:n]
	} else {
		srt = make([]uint64, n)
	}
	i := n - 1
	for _, p := range p.Progress {
		srt[i] = p.Match
		i--
	}
	insertionSort(srt)
	pos := n - (n/2 + 1)
	return srt[pos]
}

func (pr *Progress) MaybeDecrTo(rejected uint64) bool {
	// The rejection must be stale if "rejected" does not match next - 1. This
	// is because non-replicating followers are probed one entry at a time.
	if pr.Next-1 != rejected {
		return false
	}

	pr.Next = max(rejected, 1)
	return true
}

func (pr *Progress) MaybeUpdate(n uint64) bool {
	var updated bool
	if pr.Match < n {
		pr.Match = n
		updated = true
	}
	pr.Next = max(pr.Next, n+1)
	return updated
}

// VoteResult indicates the outcome of a vote.
type VoteResult uint8

const (
	// VotePending indicates that the decision of the vote depends on future
	// votes, i.e. neither "yes" or "no" has reached quorum yet.
	VotePending VoteResult = 1 + iota
	// VoteLost indicates that the quorum has voted "no".
	VoteLost
	// VoteWon indicates that the quorum has voted "yes".
	VoteWon
)
