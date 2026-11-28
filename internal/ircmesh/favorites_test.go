package ircmesh

import (
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/fakedevice"
)

// withFavorites is withNodes with AS02 (heard on #Ryfylke) and OLD (not
// heard lately) marked as favourites.
func withFavorites() *fakedevice.Device {
	d := withNodes(newFake())
	for _, n := range d.ExtraNodes {
		n.IsFavorite = true
	}
	return d
}

// setFavorite changes node num's favourite flag on the fake node, as the
// phone app would, and has the server re-read the node's state.
func (h *harness) setFavorite(num uint32, fav bool) {
	h.d.Update(func() {
		for _, n := range h.d.ExtraNodes {
			if n.GetNum() == num {
				n.IsFavorite = fav
			}
		}
	})
	h.srv.requestRefresh()
}

// isFavorite reports node num's favourite flag on the fake node.
func (h *harness) isFavorite(num uint32) bool {
	var fav bool
	h.d.Update(func() {
		for _, n := range h.d.ExtraNodes {
			if n.GetNum() == num {
				fav = n.GetIsFavorite()
			}
		}
	})
	return fav
}

func TestWelcomeAdvertisesVoiceAndLocalChannels(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.send("NICK foo")
	c.send("USER u 0 * :r")
	c.expect(`^:ircmesh 004 Ricco ircmesh test i mntv :?v$`)
	c.expect(`^:ircmesh 005 Ricco CHANTYPES=#& PREFIX=\(v\)\+ CHANMODES=b,,,mnt `)
	c.expect(`^:Ricco!ab2c9b0a@mesh JOIN :?&Favorites$`)
	c.expect(` 332 Ricco &Favorites :favourite nodes · 0 on the node · 0 heard in the last 24h · `)
	c.expect(` 353 Ricco = &Favorites :?Ricco$`)
}

func TestFavoritesAreVoiced(t *testing.T) {
	h := newHarness(t, withFavorites(), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("NAMES #Ryfylke")
	c.expect(` 353 Ricco = #Ryfylke :Ricco \+AS02$`)
	c.send("NAMES &favorites")
	c.expect(` 353 Ricco = &Favorites :Ricco \+OLD \+AS02$`)
	c.send("TOPIC &Favorites")
	c.expect(` 332 Ricco &Favorites :favourite nodes · 2 on the node · 1 heard in the last 24h · /invite or /kick to change$`)
	c.send("MODE &Favorites")
	c.expect(` 324 Ricco &Favorites :?\+mnt$`)
	c.send("LIST")
	c.expect(` 322 Ricco &Favorites 3 :favourite nodes`)
	c.send("WHO &Favorites")
	c.expect(` 352 Ricco &Favorites ab2c9b0a mesh ircmesh Ricco H :0 Ricco$`)
	c.expect(` 352 Ricco &Favorites 00000005 mesh ircmesh OLD H\+ :0 Old node$`)
	c.send("WHO AS02")
	c.expect(` 352 Ricco \* 1bf0411d mesh ircmesh AS02 H :0 Arve Tak$`)
	c.send("WHOIS AS02")
	c.expect(` 319 Ricco AS02 :\+#Ryfylke \+&Favorites$`)
	c.send("WHOIS Ricco")
	c.expect(` 319 Ricco Ricco :#LongFast #Ryfylke &Favorites$`)
}

func TestModeVoiceChangesFavorite(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("MODE #Ryfylke +v AS02")
	c.expect(`^:AS02!1bf0411d@mesh JOIN :?&Favorites$`)
	c.expect(`^:Ricco!ab2c9b0a@mesh MODE &Favorites \+v :?AS02$`)
	c.expect(`^:Ricco!ab2c9b0a@mesh MODE #Ryfylke \+v :?AS02$`)
	c.expect(`^:ircmesh TOPIC &Favorites :favourite nodes · 1 on the node · 1 heard`)
	if !h.isFavorite(arveNum) {
		t.Fatal("AS02 is not a favourite on the node")
	}
	c.send("MODE #LongFast -v AS02")
	c.expect(`^:Ricco!ab2c9b0a@mesh KICK &Favorites AS02 :no longer a favourite$`)
	c.expect(`^:Ricco!ab2c9b0a@mesh MODE #Ryfylke -v :?AS02$`)
	if h.isFavorite(arveNum) {
		t.Fatal("AS02 is still a favourite on the node")
	}
}

func TestModeVoiceSeveralAndNoOps(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("MODE &Favorites +vv AS02 OLD")
	c.expect(`MODE &Favorites \+v :?AS02$`)
	c.expect(`MODE &Favorites \+v :?OLD$`)
	n := len(h.d.Received())
	c.send("MODE #Ryfylke +v AS02") // already a favourite: nothing to do
	c.expectNone(` MODE `, 300*time.Millisecond)
	if got := len(h.d.Received()); got != n {
		t.Fatalf("%d admin messages for a no-op, want none", got-n)
	}
}

func TestModeVoiceErrors(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("MODE #Ryfylke +v nobody")
	c.expect(` 401 Ricco nobody `)
	c.send("MODE #Ryfylke +v")
	c.expect(` 461 Ricco MODE `)
	c.send("MODE #Ryfylke +v Ricco")
	c.expect(`^:ircmesh NOTICE Ricco :can't make Ricco a favourite: it's your own node$`)
	c.send("MODE #Ryfylke +m")
	c.expect(` 482 Ricco #Ryfylke :Channel modes follow the node and can't be changed$`)
	c.send("MODE &Favorites -m")
	c.expect(` 482 Ricco &Favorites :Only \+v and -v`)
	c.send("MODE &Favorites +b")
	c.expect(` 368 Ricco &Favorites `)
}

func TestFavoriteFailureIsReported(t *testing.T) {
	d := withNodes(newFake())
	d.IgnoreNodeOps = true // accepted but not applied: verifying fails
	h := newHarness(t, d, nil)
	c := h.dial(t)
	c.register("foo")
	c.send("MODE #Ryfylke +v AS02")
	c.expect(`^:ircmesh NOTICE Ricco :can't make AS02 a favourite: .*favorite`)
	c.expectNone(`JOIN :?&Favorites`, 200*time.Millisecond)
}

func TestInviteAndKickChangeFavorites(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("INVITE OLD &Favorites")
	c.expect(`^:OLD!00000005@mesh JOIN :?&Favorites$`)
	c.expect(`^:Ricco!ab2c9b0a@mesh MODE &Favorites \+v :?OLD$`)
	c.expectNone(`MODE #`, 200*time.Millisecond) // OLD is in no other channel
	c.send("INVITE OLD &Favorites")
	c.expect(` 443 Ricco OLD &Favorites `)
	c.send("INVITE AS02 #Ryfylke")
	c.expect(` 482 Ricco #Ryfylke `)
	c.send("INVITE nobody &Favorites")
	c.expect(` 401 Ricco nobody `)
	c.send("KICK &Favorites AS02")
	c.expect(` 441 Ricco AS02 &Favorites `)
	c.send("KICK #Ryfylke AS02")
	c.expect(` 482 Ricco #Ryfylke `)
	c.send("KICK &Favorites OLD :bye")
	c.expect(`^:Ricco!ab2c9b0a@mesh KICK &Favorites OLD :no longer a favourite$`)
	if h.isFavorite(oldNum) {
		t.Fatal("OLD is still a favourite on the node")
	}
}

func TestFavoriteChangedOnNode(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	h.setFavorite(arveNum, true)
	c.expect(`^:AS02!1bf0411d@mesh JOIN :?&Favorites$`)
	c.expect(`^:ircmesh MODE &Favorites \+v :?AS02$`)
	c.expect(`^:ircmesh MODE #Ryfylke \+v :?AS02$`)
	h.setFavorite(arveNum, false)
	c.expect(`^:AS02!1bf0411d@mesh PART &Favorites :no longer a favourite$`)
	c.expect(`^:ircmesh MODE #Ryfylke -v :?AS02$`)
}

func TestPartedFavoritesSeesNoChanges(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("PART &Favorites")
	c.expect(`^:Ricco!ab2c9b0a@mesh PART :?&Favorites$`)
	h.setFavorite(arveNum, true)
	c.expect(`^:ircmesh MODE #Ryfylke \+v :?AS02$`)
	c.expectNone(`&Favorites`, 200*time.Millisecond)
	c.send("JOIN &Favorites")
	c.expect(` 353 Ricco = &Favorites :Ricco \+AS02$`)
}

func TestFavoriteHeardIsVoiced(t *testing.T) {
	h := newHarness(t, withFavorites(), nil)
	c := h.dial(t)
	c.register("foo")
	h.d.Inject(rx(oldNum, 4, "back"))
	c.expect(`^:OLD!00000005@mesh JOIN :?#Ryfylke$`)
	c.expect(`^:ircmesh MODE #Ryfylke \+v :?OLD$`)
}

func TestFavoriteAgesOutWithoutQuitting(t *testing.T) {
	clk := &clock{}
	h := newHarness(t, withFavorites(), func(c *Config) { c.Now = clk.now })
	c := h.dial(t)
	c.register("foo")
	clk.advance(24 * time.Hour)
	h.srv.age()
	c.expect(`^:AS02!1bf0411d@mesh PART #Ryfylke :not heard on this channel for 24h$`)
	c.expectNone(` QUIT `, 100*time.Millisecond)
	c.send("NAMES &Favorites")
	c.expect(` 353 Ricco = &Favorites :Ricco \+OLD \+AS02$`)
}

func TestNoMessagesToFavorites(t *testing.T) {
	h := newHarness(t, withFavorites(), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("PRIVMSG &Favorites :hello")
	c.expect(` 404 Ricco &Favorites `)
	c.send("TOPIC &Favorites :new")
	c.expect(` 482 Ricco &Favorites `)
	settle()
	if n := len(h.d.Texts()); n != 0 {
		t.Fatalf("%d texts sent to the mesh", n)
	}
}

func TestFavoriteRemovedFromNodeDB(t *testing.T) {
	h := newHarness(t, withFavorites(), nil)
	c := h.dial(t)
	c.register("foo")
	h.d.Update(func() {
		h.d.ExtraNodes = []*pb.NodeInfo{h.d.ExtraNodes[0]} // OLD forgotten
	})
	h.srv.requestRefresh()
	c.expect(`^:OLD!00000005@mesh PART &Favorites :no longer a favourite$`)
}
