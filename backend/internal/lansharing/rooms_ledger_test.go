package lansharing

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"infinite-canvas/backend/internal/kernel"
)

func roomTestIdentity(t *testing.T) *Identity {
	t.Helper()
	identity, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	identity.nodeID, err = hardwareNodeID(kernel.NewID())
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestRoomMembershipLedgerAndMajorityRemoval(t *testing.T) {
	_, creator := newTestManager(t)
	member := roomTestIdentity(t)
	created, err := creator.CreateRoom(context.Background(), "制作组")
	if err != nil {
		t.Fatal(err)
	}
	genesisEvents, err := creator.roomEvents(context.Background(), created.ID)
	if err != nil || len(genesisEvents) != 1 {
		t.Fatalf("genesis events=%d err=%v", len(genesisEvents), err)
	}
	join := signRoomEvent(RoomEvent{RoomID: created.ID, Kind: "join", Parents: created.Heads, Nonce: "join-member", Subject: &RoomMember{NodeID: member.ID(), PublicKey: encodeRoomPublicKey(member.PublicKey()), Nickname: "Member"}}, creator.identity)
	joined, err := evaluateRoom(append(genesisEvents, join))
	if err != nil {
		t.Fatal(err)
	}
	if len(joined.Members) != 2 {
		t.Fatalf("members after admission = %d", len(joined.Members))
	}
	proposal := signRoomEvent(RoomEvent{RoomID: created.ID, Kind: "remove", Parents: joined.Heads, Nonce: "remove-creator", Target: creator.NodeID()}, member)
	proposed, err := evaluateRoom(append(genesisEvents, join, proposal))
	if err != nil {
		t.Fatal(err)
	}
	if proposed.Proposals[0].Passed {
		t.Fatal("one of two members must not pass a removal proposal")
	}
	if len(proposed.Proposals[0].Electorate) != 2 {
		t.Fatalf("electorate=%v", proposed.Proposals[0].Electorate)
	}
	vote := signRoomEvent(RoomEvent{RoomID: created.ID, Kind: "vote", Parents: proposed.Heads, Nonce: "vote-creator", Proposal: proposal.ID}, creator.identity)
	passed, err := evaluateRoom(append(genesisEvents, join, proposal, vote))
	if err != nil {
		t.Fatal(err)
	}
	if !passed.Proposals[0].Passed {
		t.Fatal("two of two approvals should pass")
	}
	if _, exists := passed.Members[creator.NodeID()]; exists {
		t.Fatal("removed member remains in room")
	}
	if _, exists := passed.Members[member.ID()]; !exists {
		t.Fatal("initiating member was removed")
	}
	rejoin := signRoomEvent(RoomEvent{RoomID: created.ID, Kind: "join", Parents: passed.Heads, Nonce: "readmit-creator", Subject: &RoomMember{NodeID: creator.NodeID(), PublicKey: encodeRoomPublicKey(creator.identity.PublicKey()), Nickname: "Creator"}}, member)
	rejoined, err := evaluateRoom(append(genesisEvents, join, proposal, vote, rejoin))
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := rejoined.Members[creator.NodeID()]; !exists {
		t.Fatal("approved former member could not rejoin")
	}
	if _, err := creator.CreateRoom(context.Background(), ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty name error=%v", err)
	}
	if _, err := creator.RoomAction(context.Background(), created.ID, "leave", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("last member leave error=%v", err)
	}
}

func encodeRoomPublicKey(key []byte) string { return base64.RawStdEncoding.EncodeToString(key) }

func TestRoomCatalogAndCanvasAreMemberScopedAndEqual(t *testing.T) {
	_, owner := newTestManager(t)
	_, member := newTestManager(t)
	created, err := owner.CreateRoom(context.Background(), "Shared")
	if err != nil {
		t.Fatal(err)
	}
	genesis, err := owner.roomEvents(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	join := signRoomEvent(RoomEvent{RoomID: created.ID, Kind: "join", Parents: created.Heads, Nonce: "member-join", Subject: &RoomMember{NodeID: member.NodeID(), PublicKey: encodeRoomPublicKey(member.identity.PublicKey()), Nickname: member.LocalNickname()}}, owner.identity)
	state, err := owner.mergeRoomEvents(context.Background(), created.ID, []RoomEvent{join}, false)
	if err != nil {
		t.Fatal(err)
	}
	allEvents := append(genesis, join)
	if _, err = member.mergeRoomEvents(context.Background(), created.ID, allEvents, true); err != nil {
		t.Fatal(err)
	}
	catalog, err := owner.CreateRoomCatalog(context.Background(), created.ID, "team-assets")
	if err != nil {
		t.Fatal(err)
	}
	catalogItems, err := owner.catalogMetadata(context.Background())
	if err != nil || len(catalogItems) != 1 {
		t.Fatalf("catalog metadata=%d err=%v", len(catalogItems), err)
	}
	if err = member.upsertCatalogMetadata(context.Background(), catalogItems[0]); err != nil {
		t.Fatal(err)
	}
	if err = member.CheckLocalCatalogAccess(context.Background(), catalog.ID); err != nil {
		t.Fatalf("member could not read team catalog: %v", err)
	}
	canvas, err := member.CreateRoomCollaborationCanvas(context.Background(), created.ID, "Member canvas", catalog.ID, nil)
	if err != nil {
		t.Fatalf("member could not create room canvas: %v", err)
	}
	canvasMetadata, err := member.collaborationCanvasMetadata(context.Background())
	if err != nil || len(canvasMetadata) != 1 {
		t.Fatalf("canvas metadata=%d err=%v", len(canvasMetadata), err)
	}
	if err = owner.upsertPeerCollaborationCanvasMetadata(context.Background(), member.NodeID(), canvasMetadata[0]); err != nil {
		t.Fatal(err)
	}
	updated, err := owner.UpdateCollaborationCanvas(context.Background(), canvas.ID, stringPointer("Owner edit"), nil)
	if err != nil || updated.Name != "Owner edit" {
		t.Fatalf("noncreator member update=%+v err=%v", updated, err)
	}
	if accepted, err := owner.AppendCollaborationCanvasUpdate(context.Background(), canvas.ID, []byte{1, 2, 3}); err != nil || !accepted {
		t.Fatalf("member update accepted=%v err=%v", accepted, err)
	}
	proposal := signRoomEvent(RoomEvent{RoomID: created.ID, Kind: "remove", Parents: state.Heads, Nonce: "remove-member", Target: member.NodeID()}, owner.identity)
	proposed, err := owner.mergeRoomEvents(context.Background(), created.ID, []RoomEvent{proposal}, false)
	if err != nil {
		t.Fatal(err)
	}
	vote := signRoomEvent(RoomEvent{RoomID: created.ID, Kind: "vote", Parents: proposed.Heads, Nonce: "approve-removal", Proposal: proposal.ID}, member.identity)
	final, err := owner.mergeRoomEvents(context.Background(), created.ID, []RoomEvent{vote}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = member.mergeRoomEvents(context.Background(), created.ID, []RoomEvent{proposal, vote}, false); err != nil {
		t.Fatal(err)
	}
	if _, exists := final.Members[member.NodeID()]; exists {
		t.Fatal("removed member remains active")
	}
	if err = member.CheckLocalCatalogAccess(context.Background(), catalog.ID); !errors.Is(err, ErrPeerNotTrusted) {
		t.Fatalf("removed member still reads room catalog: %v", err)
	}
	if _, err = member.CollaborationCanvasUpdates(context.Background(), canvas.ID, "", 10); !errors.Is(err, ErrPeerNotTrusted) {
		t.Fatalf("removed member still reads room canvas: %v", err)
	}
}

func stringPointer(value string) *string { return &value }
