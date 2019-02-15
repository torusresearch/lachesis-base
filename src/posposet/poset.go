package posposet

import (
	"strings"
	"sync"
)

// Poset processes events to get consensus.
type Poset struct {
	store     *Store
	state     *State
	flagTable FlagTable
	frames    map[uint64]*Frame

	processingWg   sync.WaitGroup
	processingDone chan struct{}

	newEventsCh      chan *Event
	incompleteEvents map[EventHash]*Event
}

// New creates Poset instance.
func New(store *Store) *Poset {
	const buffSize = 10

	p := &Poset{
		store:  store,
		frames: make(map[uint64]*Frame),

		newEventsCh:      make(chan *Event, buffSize),
		incompleteEvents: make(map[EventHash]*Event),
	}

	p.bootstrap()
	return p
}

// Start starts events processing. It is not safe for concurrent use.
func (p *Poset) Start() {
	if p.processingDone != nil {
		return
	}
	p.processingDone = make(chan struct{})
	p.processingWg.Add(1)
	go func() {
		defer p.processingWg.Done()
		//log.Debug("Start of events processing ...")
		for {
			select {
			case <-p.processingDone:
				//log.Debug("Stop of events processing ...")
				return
			case e := <-p.newEventsCh:
				p.onNewEvent(e)
			}
		}
	}()
}

// Stop stops events processing. It is not safe for concurrent use.
func (p *Poset) Stop() {
	if p.processingDone == nil {
		return
	}
	close(p.processingDone)
	p.processingWg.Wait()
	p.processingDone = nil
}

// PushEvent takes event into processing. Event order doesn't matter.
func (p *Poset) PushEvent(e Event) {
	initEventIdx(&e)

	p.newEventsCh <- &e
}

// onNewEvent runs consensus calc from new event. It is not safe for concurrent use.
func (p *Poset) onNewEvent(e *Event) {
	if p.store.HasEvent(e.Hash()) {
		log.WithField("event", e).Warnf("Event had received already")
		return
	}

	nodes := newParentNodesInspector(e)
	ltime := newParentLamportTimeInspector(e)

	// fill event's parents index or hold it as incompleted
	for hash := range e.Parents {
		if hash.IsZero() {
			// first event of node
			if !nodes.IsParentUnique(e.Creator) {
				return
			}
			if !ltime.IsGreaterThan(0) {
				return
			}
			continue
		}
		parent := e.parents[hash]
		if parent == nil {
			parent = p.store.GetEvent(hash)
			if parent == nil {
				//log.WithField("event", e).Debug("Event's parent has not received yet")
				p.incompleteEvents[e.Hash()] = e
				return
			}
			e.parents[hash] = parent
		}
		if !nodes.IsParentUnique(parent.Creator) {
			return
		}
		if !ltime.IsGreaterThan(parent.LamportTime) {
			return
		}
	}
	if !nodes.HasSelfParent() {
		return
	}
	if !ltime.IsSequential() {
		return
	}

	// parents OK
	p.store.SetEvent(e)
	p.consensus(e)

	// now child events may become complete, check it again
	for hash, child := range p.incompleteEvents {
		if parent, ok := child.parents[e.Hash()]; ok && parent == nil {
			child.parents[e.Hash()] = e
			delete(p.incompleteEvents, hash)
			p.onNewEvent(child)
		}
	}
}

// consensus is not safe for concurrent use.
func (p *Poset) consensus(e *Event) {
	if !p.checkIfRoot(e) {
		return
	}
	if !p.checkIfClotho(e) {
		return
	}
}

// checkIfRoot checks root-conditions for new event.
// Event.parents should be filled.
// It is not safe for concurrent use.
func (p *Poset) checkIfRoot(e *Event) bool {
	log.Debugf("----- %s", e)

	frame := p.lastNodeFrame(e.Creator)
	if frame == nil {
		frame = p.frame(p.state.LastFinishedFrameN+1, true)
	}
	log.Debugf(" last node frame: %d", frame.Index)

	knownRoots := Roots{}
	for hash, parent := range e.parents {
		if !hash.IsZero() {
			roots := frame.NodeRootsGet(parent.Creator)
			knownRoots.Add(roots)
		} else {
			roots := rootZero(e.Creator)
			knownRoots.Add(roots)
		}
	}
	frame.NodeRootsAdd(e.Creator, knownRoots)
	log.Debugf(" known %s for %s", knownRoots.String(), e.Creator.String())

	stake := p.newStakeCounter()
	for node := range knownRoots {
		stake.Count(node)
	}
	isRoot := stake.HasMajority()

	// NOTE: temporary
	if name := e.Hash().String(); (strings.ToUpper(name) == name) != isRoot {
		log.Debug(" ERR !!!!!!!!!!!!!!!!")
	}

	if !isRoot {
		frame.NodeEventAdd(e.Creator, e.Hash())
		return false
	}

	log.Debug(" selected as root")

	frame = p.frame(frame.Index+1, true)
	frame.NodeRootsAdd(e.Creator, rootFrom(e))

	for phash, parent := range e.parents {
		var roots Roots
		if !phash.IsZero() {
			roots = frame.NodeRootsGet(parent.Creator)
		} else {
			roots = rootZero(e.Creator)
		}
		frame.NodeRootsAdd(e.Creator, roots)
	}
	log.Debugf(" will known %s for %s", frame.NodeRootsGet(e.Creator).String(), e.Creator.String())

	return true
}

func (p *Poset) checkIfClotho(e *Event) bool {
	// TODO: implement it
	return false
}

/*
 * Utils:
 */

// initEventIdx initializes internal index of parents.
func initEventIdx(e *Event) {
	if e.parents != nil {
		return
	}
	e.parents = make(map[EventHash]*Event, len(e.Parents))
	for hash := range e.Parents {
		e.parents[hash] = nil
	}
}
