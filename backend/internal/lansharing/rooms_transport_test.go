package lansharing

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestRoomJoinApprovalOverDirectMutualTLS(t *testing.T) {
	if _, err := FindPrivateEndpoints(); err != nil {
		t.Skipf("private LAN adapter unavailable: %v", err)
	}
	_, host := newTestManager(t)
	_, applicant := newTestManager(t)
	hostStatus, err := host.Start(context.Background())
	if err != nil {
		t.Skipf("host LAN listener unavailable: %v", err)
	}
	if _, err := applicant.Start(context.Background()); err != nil {
		t.Skipf("applicant LAN listener unavailable: %v", err)
	}
	room, err := host.CreateRoom(context.Background(), "Direct room")
	if err != nil {
		t.Fatal(err)
	}
	address, portText, err := net.SplitHostPort(hostStatus.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	request, err := applicant.RequestRoomJoin(context.Background(), RoomJoinInput{RoomID: room.ID, PeerID: host.NodeID(), Address: address, Port: port})
	if err != nil {
		t.Fatalf("direct join request: %v", err)
	}
	requests, err := host.RoomRequests(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0].ID != request.ID || requests[0].Outgoing {
		t.Fatalf("host inbox=%+v", requests)
	}
	if _, err = host.ApproveRoomJoin(context.Background(), request.ID, true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	for {
		applicant.refreshRooms(ctx)
		rooms, err := applicant.Rooms(ctx)
		if err == nil && len(rooms) == 1 && rooms[0].Joined {
			if _, ok := rooms[0].Members[host.NodeID()]; !ok {
				t.Fatal("host membership was not imported")
			}
			if _, ok := rooms[0].Members[applicant.NodeID()]; !ok {
				t.Fatal("approved applicant is not a member")
			}
			return
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatalf("approved membership did not sync: rooms=%+v err=%v", rooms, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestRemovedRoomMemberReceivesLedgerReadOnly(t *testing.T) {
	_, host := newTestManager(t)
	_, formerMember := newTestManager(t)
	room, err := host.CreateRoom(context.Background(), "Members")
	if err != nil {
		t.Fatal(err)
	}
	join := signRoomEvent(RoomEvent{RoomID: room.ID, Kind: "join", Parents: room.Heads, Nonce: "join", Subject: &RoomMember{
		NodeID: formerMember.NodeID(), PublicKey: encodeRoomPublicKey(formerMember.identity.PublicKey()), Nickname: "Former member",
	}}, host.identity)
	joined, err := host.mergeRoomEvents(context.Background(), room.ID, []RoomEvent{join}, false)
	if err != nil {
		t.Fatal(err)
	}
	staleEvents, err := host.roomEvents(context.Background(), room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = formerMember.mergeRoomEvents(context.Background(), room.ID, staleEvents, true); err != nil {
		t.Fatal(err)
	}
	proposal := signRoomEvent(RoomEvent{RoomID: room.ID, Kind: "remove", Parents: joined.Heads, Nonce: "remove", Target: formerMember.NodeID()}, host.identity)
	proposed, err := host.mergeRoomEvents(context.Background(), room.ID, []RoomEvent{proposal}, false)
	if err != nil {
		t.Fatal(err)
	}
	vote := signRoomEvent(RoomEvent{RoomID: room.ID, Kind: "vote", Parents: proposed.Heads, Nonce: "approve", Proposal: proposal.ID}, formerMember.identity)
	if _, err = host.mergeRoomEvents(context.Background(), room.ID, []RoomEvent{vote}, false); err != nil {
		t.Fatal(err)
	}
	unauthorized := signRoomEvent(RoomEvent{RoomID: room.ID, Kind: "profile", Parents: joined.Heads, Nonce: "stale-profile", Name: "Stale update"}, formerMember.identity)
	submittedEvents := append(append([]RoomEvent(nil), staleEvents...), unauthorized)
	body, err := json.Marshal(map[string]any{"events": submittedEvents})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/rooms/"+room.ID+"/events", bytes.NewReader(body))
	request.SetPathValue("roomID", room.ID)
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{
		Subject:   pkix.Name{CommonName: formerMember.NodeID()},
		PublicKey: formerMember.identity.PublicKey(),
	}}}
	response := httptest.NewRecorder()
	host.exchangeRoomEvents(response, request)
	if response.Code != 200 {
		t.Fatalf("removed member ledger exchange status=%d body=%s", response.Code, response.Body.String())
	}
	var reply struct {
		Events []RoomEvent `json:"events"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	state, err := evaluateRoom(reply.Events)
	if err != nil {
		t.Fatal(err)
	}
	if _, stillMember := state.Members[formerMember.NodeID()]; stillMember {
		t.Fatal("former member did not receive the removal record")
	}
	for _, event := range reply.Events {
		if event.ID == unauthorized.ID {
			t.Fatal("former member write was merged after removal")
		}
	}
	if _, err = formerMember.mergeRoomEvents(context.Background(), room.ID, reply.Events, false); err != nil {
		t.Fatal(err)
	}
	formerRooms, err := formerMember.Rooms(context.Background())
	if err != nil || len(formerRooms) != 1 || formerRooms[0].Joined {
		t.Fatalf("removed member local state=%+v err=%v", formerRooms, err)
	}
}

func TestSortRoomLocationsDeduplicatesRoomAcrossMembers(t *testing.T) {
	rooms := sortRoomLocations([]RoomLocation{
		{ID: "room-b", Code: "00000001", PeerID: "peer-b"},
		{ID: "room-a", Code: "00000002", PeerID: "peer-z"},
		{ID: "room-a", Code: "00000002", PeerID: "peer-a"},
		{ID: "room-c", Code: "00000002", PeerID: "peer-c"},
	})
	if len(rooms) != 3 {
		t.Fatalf("discovered rooms=%+v", rooms)
	}
	if rooms[0].ID != "room-a" || rooms[0].PeerID != "peer-a" || rooms[1].ID != "room-b" || rooms[2].ID != "room-c" {
		t.Fatalf("unexpected deterministic room list: %+v", rooms)
	}
}
