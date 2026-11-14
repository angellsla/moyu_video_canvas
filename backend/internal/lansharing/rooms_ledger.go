package lansharing

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

// Events reference their causal parents, so independent admissions and votes
// merge without a coordinator. Signatures cover every field including parents.
type RoomEvent struct {
	ID        string      `json:"id"`
	RoomID    string      `json:"roomId"`
	Kind      string      `json:"kind"`
	Author    string      `json:"author"`
	PublicKey string      `json:"publicKey"`
	Parents   []string    `json:"parents"`
	Nonce     string      `json:"nonce"`
	Name      string      `json:"name,omitempty"`
	Code      string      `json:"code,omitempty"`
	Subject   *RoomMember `json:"subject,omitempty"`
	Proposal  string      `json:"proposal,omitempty"`
	Target    string      `json:"target,omitempty"`
	Signature string      `json:"signature"`
}
type RoomMember struct {
	NodeID    string `json:"nodeId"`
	PublicKey string `json:"publicKey"`
	Nickname  string `json:"nickname"`
}
type RoomProposal struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Target     string   `json:"target,omitempty"`
	Electorate []string `json:"electorate"`
	Approvals  []string `json:"approvals"`
	Passed     bool     `json:"passed"`
}
type RoomState struct {
	ID        string                `json:"id"`
	Code      string                `json:"code"`
	Name      string                `json:"name"`
	Members   map[string]RoomMember `json:"members"`
	Proposals []RoomProposal        `json:"proposals"`
	Dissolved bool                  `json:"dissolved"`
	Heads     []string              `json:"heads"`
}
type roomGraph struct {
	events      map[string]RoomEvent
	ancestors   map[string]map[string]bool
	electorates map[string][]string
}

func roomEventBytes(e RoomEvent) []byte {
	e.ID, e.Signature = "", ""
	encoded, _ := json.Marshal(e)
	return encoded
}
func signRoomEvent(e RoomEvent, identity *Identity) RoomEvent {
	e.Author, e.PublicKey = identity.ID(), base64.RawStdEncoding.EncodeToString(identity.PublicKey())
	body := roomEventBytes(e)
	hash := sha256.Sum256(body)
	e.ID = hex.EncodeToString(hash[:])
	e.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(identity.privateKey, body))
	return e
}
func verifyRoomEvent(e RoomEvent) bool {
	key, err := base64.RawStdEncoding.DecodeString(e.PublicKey)
	sig, sigErr := base64.RawStdEncoding.DecodeString(e.Signature)
	hash := sha256.Sum256(roomEventBytes(e))
	return err == nil && sigErr == nil && len(key) == ed25519.PublicKeySize && validNodeID(e.Author) &&
		e.ID == hex.EncodeToString(hash[:]) && ed25519.Verify(key, roomEventBytes(e), sig)
}
func validRoomCode(code string) bool {
	if len(code) != 8 {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
func validRoomMember(member *RoomMember) bool {
	if member == nil || !validNodeID(member.NodeID) || len([]rune(member.Nickname)) > 80 {
		return false
	}
	key, err := base64.RawStdEncoding.DecodeString(member.PublicKey)
	return err == nil && len(key) == ed25519.PublicKeySize
}

// Validate against each event's causal membership, not wall-clock time or
// receipt order. Departures and majority removals are permanent tombstones.
// After a departure, only admissions in its causal past keep the departing
// member's sponsorship; backdated grants cannot extend its authority.
func evaluateRoom(events []RoomEvent) (RoomState, error) {
	if len(events) == 0 || len(events) > 4096 {
		return RoomState{}, ErrInvalid
	}
	g := roomGraph{events: map[string]RoomEvent{}, ancestors: map[string]map[string]bool{}, electorates: map[string][]string{}}
	var root RoomEvent
	for _, e := range events {
		if !verifyRoomEvent(e) || len(e.Parents) > 256 || len(e.Nonce) > 80 {
			return RoomState{}, ErrInvalid
		}
		if _, exists := g.events[e.ID]; exists {
			return RoomState{}, ErrConflict
		}
		g.events[e.ID] = e
		if e.Kind == "create" {
			if root.ID != "" || e.RoomID != "" || len(e.Parents) != 0 || !validRoomCode(e.Code) || strings.TrimSpace(e.Name) == "" || len([]rune(e.Name)) > 80 || !validRoomMember(e.Subject) || e.Subject.NodeID != e.Author || e.Subject.PublicKey != e.PublicKey {
				return RoomState{}, ErrInvalid
			}
			root = e
		}
	}
	if root.ID == "" {
		return RoomState{}, ErrInvalid
	}
	pending := make(map[string]bool, len(events))
	for id := range g.events {
		pending[id] = true
	}
	for len(pending) > 0 {
		progressed := false
		for id := range pending {
			e := g.events[id]
			ancestors := map[string]bool{}
			ready := true
			for _, parent := range e.Parents {
				prior, ok := g.ancestors[parent]
				if !ok {
					ready = false
					break
				}
				ancestors[parent] = true
				for ancestor := range prior {
					ancestors[ancestor] = true
				}
			}
			if !ready {
				continue
			}
			if id != root.ID {
				if e.RoomID != root.ID || len(e.Parents) == 0 || !ancestors[root.ID] {
					return RoomState{}, ErrInvalid
				}
				before := g.reduce(root, ancestors)
				member, active := before.Members[e.Author]
				if !active || before.Dissolved || member.PublicKey != e.PublicKey {
					return RoomState{}, ErrPeerNotTrusted
				}
				switch e.Kind {
				case "join":
					if !validRoomMember(e.Subject) {
						return RoomState{}, ErrInvalid
					}
					if old, exists := before.Members[e.Subject.NodeID]; exists && old.PublicKey != e.Subject.PublicKey {
						return RoomState{}, ErrConflict
					}
				case "leave":
					if e.Target != e.Author {
						return RoomState{}, ErrInvalid
					}
				case "profile":
					if strings.TrimSpace(e.Name) == "" || len([]rune(e.Name)) > 80 {
						return RoomState{}, ErrInvalid
					}
				case "remove", "dissolve":
					if e.Kind == "remove" {
						if _, ok := before.Members[e.Target]; !ok || e.Target == e.Author {
							return RoomState{}, ErrInvalid
						}
					}
					for memberID := range before.Members {
						g.electorates[id] = append(g.electorates[id], memberID)
					}
					sort.Strings(g.electorates[id])
				case "vote":
					proposal, exists := g.events[e.Proposal]
					if !exists || !ancestors[e.Proposal] || (proposal.Kind != "remove" && proposal.Kind != "dissolve") || !containsString(g.electorates[e.Proposal], e.Author) {
						return RoomState{}, ErrInvalid
					}
				default:
					return RoomState{}, ErrInvalid
				}
			}
			g.ancestors[id] = ancestors
			delete(pending, id)
			progressed = true
		}
		if !progressed {
			return RoomState{}, ErrInvalid
		}
	}
	all := map[string]bool{}
	heads := map[string]bool{}
	for id := range g.events {
		all[id], heads[id] = true, true
	}
	for _, e := range g.events {
		for _, parent := range e.Parents {
			delete(heads, parent)
		}
	}
	result := g.reduce(root, all)
	for id := range heads {
		result.Heads = append(result.Heads, id)
	}
	sort.Strings(result.Heads)
	return result, nil
}
func containsString(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
func (g roomGraph) reduce(root RoomEvent, included map[string]bool) RoomState {
	result := RoomState{ID: root.ID, Code: root.Code, Name: root.Name, Members: map[string]RoomMember{}, Proposals: []RoomProposal{}}
	removed := map[string]bool{}
	protected := map[string]map[string]bool{}
	revocations := map[string][]string{}
	proposals := map[string]*RoomProposal{}
	for id := range included {
		e := g.events[id]
		if e.Kind == "remove" || e.Kind == "dissolve" {
			proposals[id] = &RoomProposal{ID: id, Kind: e.Kind, Target: e.Target, Electorate: g.electorates[id], Approvals: []string{e.Author}}
		}
	}
	for id := range included {
		e := g.events[id]
		if e.Kind == "vote" {
			if p := proposals[e.Proposal]; p != nil && !containsString(p.Approvals, e.Author) {
				p.Approvals = append(p.Approvals, e.Author)
			}
		}
	}
	for _, p := range proposals {
		p.Passed = len(p.Electorate) > 0 && len(p.Approvals) > len(p.Electorate)/2
		sort.Strings(p.Approvals)
		result.Proposals = append(result.Proposals, *p)
		if p.Passed {
			if p.Kind == "dissolve" {
				result.Dissolved = true
			} else {
				removed[p.Target] = true
				revocations[p.Target] = append(revocations[p.Target], p.ID)
				if previous, exists := protected[p.Target]; exists {
					intersection := map[string]bool{}
					for ancestor := range previous {
						if g.ancestors[p.ID][ancestor] {
							intersection[ancestor] = true
						}
					}
					protected[p.Target] = intersection
				} else {
					protected[p.Target] = g.ancestors[p.ID]
				}
			}
		}
	}
	for id := range included {
		e := g.events[id]
		if e.Kind == "leave" {
			removed[e.Author] = true
			revocations[e.Author] = append(revocations[e.Author], id)
			if previous, ok := protected[e.Author]; ok {
				intersection := map[string]bool{}
				for ancestor := range previous {
					if g.ancestors[id][ancestor] {
						intersection[ancestor] = true
					}
				}
				protected[e.Author] = intersection
			} else {
				protected[e.Author] = g.ancestors[id]
			}
		}
	}
	if !removed[root.Author] {
		result.Members[root.Author] = *root.Subject
	}
	// Certificates are retained after their sponsor leaves, provided that the
	// admission was known at departure. Unknown concurrent grants are discarded.
	ids := make([]string, 0, len(included))
	for id := range included {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for round := 0; round < len(ids); round++ {
		changed := false
		for _, id := range ids {
			e := g.events[id]
			if e.Kind != "join" || e.Subject == nil {
				continue
			}
			_, authorActive := result.Members[e.Author]
			if !authorActive && !(removed[e.Author] && protected[e.Author][id]) {
				continue
			}
			if removed[e.Author] && !protected[e.Author][id] {
				continue
			}
			if removed[e.Subject.NodeID] {
				mayRejoin := len(revocations[e.Subject.NodeID]) > 0
				for _, revocation := range revocations[e.Subject.NodeID] {
					if !g.ancestors[id][revocation] {
						mayRejoin = false
						break
					}
				}
				for proposalID, proposal := range proposals {
					if proposal.Target != e.Subject.NodeID || !proposal.Passed {
						continue
					}
					for eventID, vote := range g.events {
						if vote.Kind == "vote" && vote.Proposal == proposalID && included[eventID] && !g.ancestors[id][eventID] {
							mayRejoin = false
							break
						}
					}
				}
				if !mayRejoin {
					continue
				}
				removed[e.Subject.NodeID] = false
			}
			if _, exists := result.Members[e.Subject.NodeID]; !exists {
				result.Members[e.Subject.NodeID] = *e.Subject
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	profiles := map[string][]RoomEvent{}
	for _, id := range ids {
		event := g.events[id]
		if event.Kind == "profile" {
			profiles[event.Author] = append(profiles[event.Author], event)
		}
	}
	for author, candidates := range profiles {
		member, exists := result.Members[author]
		if !exists {
			continue
		}
		maximal := make([]RoomEvent, 0, len(candidates))
		for _, candidate := range candidates {
			dominated := false
			for _, other := range candidates {
				if other.ID != candidate.ID && g.ancestors[other.ID][candidate.ID] {
					dominated = true
					break
				}
			}
			if !dominated {
				maximal = append(maximal, candidate)
			}
		}
		if len(maximal) == 0 {
			continue
		}
		sort.Slice(maximal, func(i, j int) bool { return maximal[i].ID < maximal[j].ID })
		member.Nickname = maximal[len(maximal)-1].Name
		result.Members[author] = member
	}
	sort.Slice(result.Proposals, func(i, j int) bool { return result.Proposals[i].ID < result.Proposals[j].ID })
	return result
}
