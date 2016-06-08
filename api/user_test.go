// Copyright (c) 2015 Mattermost, Inc. All Rights Reserved.
// See License.txt for license information.

package api

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/goamz/goamz/aws"
	"github.com/goamz/goamz/s3"

	"github.com/mattermost/platform/model"
	"github.com/mattermost/platform/store"
	"github.com/mattermost/platform/utils"
)

func getMockContext() *Context {
	return &Context{
		RequestId: model.NewId(),
		IpAddress: "tests",
		T:         utils.TfuncWithFallback(model.DEFAULT_LOCALE),
		Locale:    model.DEFAULT_LOCALE,
	}
}

// usage: at the beginning of a test function, use defer as follows (note the
// double ()):
// defer backupAndRestoreCfg()()
// backupAndRestoreCfg() will be evaluated when the defer executes, not when
// the call executes, thus backuping the config when defer executes. Before the
// calling function returns, the config will be restored.
func backupAndRestoreCfg() func() {
	oldCfg := *utils.Cfg
	return func() { utils.Cfg = &oldCfg }
}

func invitationLinkDateString(valid bool) string {
	// TODO: the validity period should be a constant somewhere
	var daysAgo time.Duration
	if valid {
		daysAgo = time.Hour * 24 * 1
	} else {
		daysAgo = time.Hour * 24 * 3
	}
	date := time.Now().Add(-daysAgo).UnixNano() / int64(time.Millisecond)
	return strconv.FormatInt(date, 10)
}

func TestUser_createUser_emailDisabled(t *testing.T) {
	defer backupAndRestoreCfg()()
	c := getMockContext()
	w := httptest.NewRecorder()
	r, err := http.NewRequest("POST", "http://example.com", nil)
	if err != nil {
		t.Fatal("Couldn't create a request: ", err)
	}

	flagsList := []struct {
		EnableSignUpWithEmail bool
		EnableUserCreation    bool
	}{
		{true, false},
		{false, true},
		{false, false},
	}

	for _, flags := range flagsList {
		currentTest := fmt.Sprintf("EnableSignUpWithEmail: %t, EnableUserCreation: %t",
			flags.EnableSignUpWithEmail, flags.EnableUserCreation)
		utils.Cfg.EmailSettings.EnableSignUpWithEmail = flags.EnableSignUpWithEmail
		utils.Cfg.TeamSettings.EnableUserCreation = flags.EnableUserCreation
		createUser(c, w, r)
		if c.Err == nil {
			t.Fatalf("c.Err shouldn't be nil for %s", currentTest)
		}
		if got, want := c.Err.StatusCode, http.StatusNotImplemented; got != want {
			t.Fatalf("StatusCode, got: %v, want: %v for %s", got, want, currentTest)
		}
	}
}

func TestUser_createUser_invalidLink(t *testing.T) {
	defer backupAndRestoreCfg()()
	utils.Cfg.EmailSettings.EnableSignUpWithEmail = true
	utils.Cfg.TeamSettings.EnableUserCreation = true
	utils.Cfg.EmailSettings.InviteSalt = "fake salt"
	c := getMockContext()
	w := httptest.NewRecorder()
	u, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatal("Couldn't create URL: ", err)
	}
	// make sure hash comparison will fail
	v := url.Values{}
	v.Set("h", "fake hash")
	v.Set("d", "fake data")
	u.RawQuery = v.Encode()
	r, err := http.NewRequest("POST", u.String(), strings.NewReader("{}"))
	if err != nil {
		t.Fatal("Couldn't create a request: ", err)
	}

	createUser(c, w, r)
	if c.Err == nil {
		t.Fatalf("c.Err shouldn't be nil")
	}
	if got, want := c.Err.Id, "api.user.create_user.signup_link_invalid.app_error"; got != want {
		t.Fatalf("c.Err.Id, got: %v, want: %v", got, want)
	}
}

func TestUser_createUser_expiredLink(t *testing.T) {
	defer backupAndRestoreCfg()()
	utils.Cfg.EmailSettings.EnableSignUpWithEmail = true
	utils.Cfg.TeamSettings.EnableUserCreation = true
	utils.Cfg.EmailSettings.InviteSalt = "fake salt"
	c := getMockContext()
	w := httptest.NewRecorder()
	u, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatal("Couldn't create URL: ", err)
	}
	// make sure the link is expired
	d := `{"display_name":"teamName","email":"fake@example.com","id":"aFakeTeamID","name":"teamName","time":"` + invitationLinkDateString(false) + `"}`
	// make sure hash comparison succeeds
	h := model.HashPassword(fmt.Sprintf("%v:%v", d, utils.Cfg.EmailSettings.InviteSalt))
	v := url.Values{}
	v.Set("h", h)
	v.Set("d", d)
	u.RawQuery = v.Encode()
	r, err := http.NewRequest("POST", u.String(), strings.NewReader("{}"))
	if err != nil {
		t.Fatal("Couldn't create a request: ", err)
	}

	createUser(c, w, r)
	if c.Err == nil {
		t.Fatalf("c.Err shouldn't be nil")
	}
	if got, want := c.Err.Id, "api.user.create_user.signup_link_expired.app_error"; got != want {
		t.Fatalf("c.Err.Id, got: %v, want: %v", got, want)
	}
}

func TestUser_createUser_invalidTeamID(t *testing.T) {
	Setup()
	defer backupAndRestoreCfg()()
	utils.Cfg.EmailSettings.EnableSignUpWithEmail = true
	utils.Cfg.TeamSettings.EnableUserCreation = true
	utils.Cfg.EmailSettings.InviteSalt = "fake salt"
	c := getMockContext()
	w := httptest.NewRecorder()
	u, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatal("Couldn't create URL: ", err)
	}
	// make sure the link is not expired
	d := `{"display_name":"teamName","email":"fake@example.com","id":"aFakeTeamID","name":"teamName","time":"` + invitationLinkDateString(true) + `"}`
	// make sure hash comparison succeeds
	h := model.HashPassword(fmt.Sprintf("%v:%v", d, utils.Cfg.EmailSettings.InviteSalt))
	v := url.Values{}
	v.Set("h", h)
	v.Set("d", d)
	u.RawQuery = v.Encode()
	r, err := http.NewRequest("POST", u.String(), strings.NewReader("{}"))
	if err != nil {
		t.Fatal("Couldn't create a request: ", err)
	}

	createUser(c, w, r)
	if c.Err == nil {
		t.Fatalf("c.Err shouldn't be nil")
	}
	if got, want := c.Err.Where, "SqlTeamStore.Get"; got != want {
		t.Fatalf("c.Err.Where, got: %v, want: %v", got, want)
	}
}

func TestUser_createUser_invalidInviteID(t *testing.T) {
	Setup()
	defer backupAndRestoreCfg()()
	utils.Cfg.EmailSettings.EnableSignUpWithEmail = true
	utils.Cfg.TeamSettings.EnableUserCreation = true
	utils.Cfg.EmailSettings.InviteSalt = "fake salt"
	c := getMockContext()
	w := httptest.NewRecorder()
	u, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatal("Couldn't create URL: ", err)
	}
	// make sure there's an invite ID and nothing else
	v := url.Values{}
	v.Set("iid", "fake invite id")
	u.RawQuery = v.Encode()
	r, err := http.NewRequest("POST", u.String(), strings.NewReader("{}"))
	if err != nil {
		t.Fatal("Couldn't create a request: ", err)
	}

	createUser(c, w, r)
	if c.Err == nil {
		t.Fatalf("c.Err shouldn't be nil")
	}
	if got, want := c.Err.Where, "SqlTeamStore.GetByInviteId"; got != want {
		t.Fatalf("c.Err.Where, got: %v, want: %v", got, want)
	}
}

func TestUser_createUser_invalidDomain(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	defer backupAndRestoreCfg()()
	utils.Cfg.EmailSettings.EnableSignUpWithEmail = true
	utils.Cfg.TeamSettings.EnableUserCreation = true
	utils.Cfg.EmailSettings.InviteSalt = "fake salt"
	utils.Cfg.TeamSettings.RestrictCreationToDomains = "mattermost.org"

	inviteID := model.NewId()
	team := &model.Team{
		DisplayName: "Name",
		Name:        "z-z-" + model.NewId() + "a",
		Email:       "test@mattermost.org",
		Type:        model.TEAM_OPEN,
		InviteId:    inviteID,
	}
	rteam, err1 := Client.CreateTeam(team)
	if err1 != nil {
		t.Fatal("Couldn't create team: ", err1)
	}
	team = rteam.Data.(*model.Team)

	c := getMockContext()
	w := httptest.NewRecorder()

	u, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatal("Couldn't create URL: ", err)
	}

	// make sure the link is not expired and the domain is not in the restricted list
	d := `{"display_name":"teamName","email":"fake@example.com","id":"` + team.Id + `","name":"teamName","time":"` + invitationLinkDateString(true) + `"}`
	// make sure hash comparison succeeds
	h := model.HashPassword(fmt.Sprintf("%v:%v", d, utils.Cfg.EmailSettings.InviteSalt))
	v := url.Values{}
	v.Set("h", h)
	v.Set("d", d)
	v.Set("iid", team.InviteId)
	u.RawQuery = v.Encode()
	r, err := http.NewRequest("POST", u.String(), strings.NewReader("{}"))
	if err != nil {
		t.Fatal("Couldn't create a request: ", err)
	}

	createUser(c, w, r)
	if c.Err == nil {
		t.Fatalf("c.Err shouldn't be nil")
	}
	if got, want := c.Err.Id, "api.user.create_user.accepted_domain.app_error"; got != want {
		t.Fatalf("c.Err.Id, got: %v, want: %v", got, want)
	}
}

func TestUser_createUser(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	defer backupAndRestoreCfg()()
	utils.Cfg.EmailSettings.EnableSignUpWithEmail = true
	utils.Cfg.TeamSettings.EnableUserCreation = true
	utils.Cfg.EmailSettings.InviteSalt = "fake salt"

	inviteID := model.NewId()
	team := &model.Team{
		DisplayName: "Name",
		Name:        "z-z-" + model.NewId() + "a",
		Email:       "test@example.org",
		Type:        model.TEAM_OPEN,
		InviteId:    inviteID,
	}
	rteam, err1 := Client.CreateTeam(team)
	if err1 != nil {
		t.Fatal("Couldn't create team: ", err1)
	}
	team = rteam.Data.(*model.Team)

	c := getMockContext()
	w := httptest.NewRecorder()

	u, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatal("Couldn't create URL: ", err)
	}

	d := `{"display_name":"teamName","email":"fake+` + model.NewId() + `@example.com","id":"` + team.Id + `","name":"teamName","time":"` + invitationLinkDateString(true) + `"}`
	// make sure hash comparison succeeds
	h := model.HashPassword(fmt.Sprintf("%v:%v", d, utils.Cfg.EmailSettings.InviteSalt))
	v := url.Values{}
	v.Set("h", h)
	v.Set("d", d)
	v.Set("iid", team.InviteId)
	u.RawQuery = v.Encode()
	r, err := http.NewRequest("POST", u.String(), strings.NewReader("{}"))
	if err != nil {
		t.Fatal("Couldn't create a request: ", err)
	}

	createUser(c, w, r)
	if c.Err != nil {
		t.Fatalf("c.Err should be nil but is %v", c.Err)
	}
	if got, want := w.Code, http.StatusOK; got != want {
		t.Fatalf("StatusCode, got: %v, want: %v", got, want)
	}
}

func TestCreateUser(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	team := model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	rteam, _ := Client.CreateTeam(&team)

	user := model.User{Email: strings.ToLower("success+"+model.NewId()) + "@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "hello", Username: "n" + model.NewId()}

	ruser, err := Client.CreateUser(&user, "")
	if err != nil {
		t.Fatal(err)
	}

	LinkUserToTeam(ruser.Data.(*model.User), rteam.Data.(*model.Team))

	if ruser.Data.(*model.User).Nickname != user.Nickname {
		t.Fatal("nickname didn't match")
	}

	if ruser.Data.(*model.User).Password != "" {
		t.Fatal("password wasn't blank")
	}

	if _, err := Client.CreateUser(ruser.Data.(*model.User), ""); err == nil {
		t.Fatal("Cannot create an existing")
	}

	ruser.Data.(*model.User).Id = ""
	ruser.Data.(*model.User).Username = "n" + model.NewId()
	if _, err := Client.CreateUser(ruser.Data.(*model.User), ""); err != nil {
		if err.Message != "An account with that email already exists." {
			t.Fatal(err)
		}
	}

	ruser.Data.(*model.User).Email = "success+" + model.NewId() + "@simulator.amazonses.com"
	ruser.Data.(*model.User).Username = user.Username
	if _, err := Client.CreateUser(ruser.Data.(*model.User), ""); err != nil {
		if err.Message != "An account with that username already exists." {
			t.Fatal(err)
		}
	}

	ruser.Data.(*model.User).Email = ""
	if _, err := Client.CreateUser(ruser.Data.(*model.User), ""); err != nil {
		if err.Message != "Invalid email" {
			t.Fatal(err)
		}
	}

	if _, err := Client.DoApiPost("/users/create", "garbage"); err == nil {
		t.Fatal("should have been an error")
	}
}

func TestLogin(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	enableSignInWithEmail := *utils.Cfg.EmailSettings.EnableSignInWithEmail
	enableSignInWithUsername := *utils.Cfg.EmailSettings.EnableSignInWithUsername
	enableLdap := *utils.Cfg.LdapSettings.Enable
	defer func() {
		*utils.Cfg.EmailSettings.EnableSignInWithEmail = enableSignInWithEmail
		*utils.Cfg.EmailSettings.EnableSignInWithUsername = enableSignInWithUsername
		*utils.Cfg.LdapSettings.Enable = enableLdap
	}()

	*utils.Cfg.EmailSettings.EnableSignInWithEmail = false
	*utils.Cfg.EmailSettings.EnableSignInWithUsername = false
	*utils.Cfg.LdapSettings.Enable = false

	team := model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	rteam, _ := Client.CreateTeam(&team)

	user := model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Username: "corey" + model.NewId(), Password: "pwd"}
	ruser, _ := Client.CreateUser(&user, "")
	LinkUserToTeam(ruser.Data.(*model.User), rteam.Data.(*model.Team))
	store.Must(Srv.Store.User().VerifyEmail(ruser.Data.(*model.User).Id))

	if result, err := Client.LoginById(ruser.Data.(*model.User).Id, user.Password); err != nil {
		t.Fatal(err)
	} else {
		if result.Data.(*model.User).Email != user.Email {
			t.Fatal("emails didn't match")
		}
	}

	if _, err := Client.Login(user.Email, user.Password); err == nil {
		t.Fatal("shouldn't be able to log in by email when disabled")
	}

	*utils.Cfg.EmailSettings.EnableSignInWithEmail = true
	if result, err := Client.Login(user.Email, user.Password); err != nil {
		t.Fatal(err)
	} else {
		if result.Data.(*model.User).Email != user.Email {
			t.Fatal("emails didn't match")
		}
	}

	if _, err := Client.Login(user.Username, user.Password); err == nil {
		t.Fatal("shouldn't be able to log in by username when disabled")
	}

	*utils.Cfg.EmailSettings.EnableSignInWithUsername = true
	if result, err := Client.Login(user.Username, user.Password); err != nil {
		t.Fatal(err)
	} else {
		if result.Data.(*model.User).Email != user.Email {
			t.Fatal("emails didn't match")
		}
	}

	if _, err := Client.Login(user.Email, user.Password+"invalid"); err == nil {
		t.Fatal("Invalid Password")
	}

	if _, err := Client.Login(user.Username, user.Password+"invalid"); err == nil {
		t.Fatal("Invalid Password")
	}

	if _, err := Client.Login("", user.Password); err == nil {
		t.Fatal("should have failed")
	}

	if _, err := Client.Login("", user.Password); err == nil {
		t.Fatal("should have failed")
	}

	authToken := Client.AuthToken
	Client.AuthToken = "invalid"

	if _, err := Client.GetUser(ruser.Data.(*model.User).Id, ""); err == nil {
		t.Fatal("should have failed")
	}

	Client.AuthToken = ""

	team2 := model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_INVITE}
	rteam2 := Client.Must(Client.CreateTeam(&team2))

	user2 := model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}

	if _, err := Client.CreateUserFromSignup(&user2, "junk", "1231312"); err == nil {
		t.Fatal("Should have errored, signed up without hashed email")
	}

	props := make(map[string]string)
	props["email"] = user2.Email
	props["id"] = rteam2.Data.(*model.Team).Id
	props["display_name"] = rteam2.Data.(*model.Team).DisplayName
	props["time"] = fmt.Sprintf("%v", model.GetMillis())
	data := model.MapToJson(props)
	hash := model.HashPassword(fmt.Sprintf("%v:%v", data, utils.Cfg.EmailSettings.InviteSalt))

	ruser2, _ := Client.CreateUserFromSignup(&user2, data, hash)

	if _, err := Client.Login(ruser2.Data.(*model.User).Email, user2.Password); err != nil {
		t.Fatal("From verfied hash")
	}

	Client.AuthToken = authToken

	user3 := &model.User{
		Email:       strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com",
		Nickname:    "Corey Hulen",
		Username:    "corey" + model.NewId(),
		Password:    "pwd",
		AuthService: model.USER_AUTH_SERVICE_LDAP,
	}
	user3 = Client.Must(Client.CreateUser(user3, "")).Data.(*model.User)
	store.Must(Srv.Store.User().VerifyEmail(user3.Id))

	if _, err := Client.Login(user3.Id, user3.Password); err == nil {
		t.Fatal("LDAP user should not be able to log in with LDAP disabled")
	}
}

func TestLoginByLdap(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	team := model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	rteam, _ := Client.CreateTeam(&team)

	user := model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Username: "corey" + model.NewId(), Password: "pwd"}
	ruser, _ := Client.CreateUser(&user, "")
	LinkUserToTeam(ruser.Data.(*model.User), rteam.Data.(*model.Team))
	store.Must(Srv.Store.User().VerifyEmail(ruser.Data.(*model.User).Id))

	if _, err := Client.LoginByLdap(ruser.Data.(*model.User).Id, user.Password); err == nil {
		t.Fatal("should've failed to log in with non-ldap user")
	}
}

func TestLoginWithDeviceId(t *testing.T) {
	th := Setup().InitBasic()
	Client := th.BasicClient
	user := th.BasicUser
	Client.Must(Client.Logout())

	deviceId := model.NewId()
	if result, err := Client.LoginWithDevice(user.Email, user.Password, deviceId); err != nil {
		t.Fatal(err)
	} else {
		ruser := result.Data.(*model.User)

		if ssresult, err := Client.GetSessions(ruser.Id); err != nil {
			t.Fatal(err)
		} else {
			sessions := ssresult.Data.([]*model.Session)
			if _, err := Client.LoginWithDevice(user.Email, user.Password, deviceId); err != nil {
				t.Fatal(err)
			}

			if sresult := <-Srv.Store.Session().Get(sessions[0].Id); sresult.Err == nil {
				t.Fatal("session should have been removed")
			}
		}
	}
}

func TestSessions(t *testing.T) {
	th := Setup().InitBasic()
	Client := th.BasicClient
	user := th.BasicUser
	Client.Must(Client.Logout())

	deviceId := model.NewId()
	Client.LoginWithDevice(user.Email, user.Password, deviceId)
	Client.Login(user.Email, user.Password)

	r1, err := Client.GetSessions(user.Id)
	if err != nil {
		t.Fatal(err)
	}

	sessions := r1.Data.([]*model.Session)
	otherSession := ""

	if len(sessions) != 2 {
		t.Fatal("invalid number of sessions")
	}

	for _, session := range sessions {
		if session.DeviceId == deviceId {
			otherSession = session.Id
		}

		if len(session.Token) != 0 {
			t.Fatal("shouldn't return session tokens")
		}
	}

	if _, err := Client.RevokeSession(otherSession); err != nil {
		t.Fatal(err)
	}

	r2, err := Client.GetSessions(user.Id)
	if err != nil {
		t.Fatal(err)
	}

	sessions2 := r2.Data.([]*model.Session)

	if len(sessions2) != 1 {
		t.Fatal("invalid number of sessions")
	}
}

func TestGetUser(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	team := model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	rteam, _ := Client.CreateTeam(&team)

	user := model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	ruser, _ := Client.CreateUser(&user, "")
	LinkUserToTeam(ruser.Data.(*model.User), rteam.Data.(*model.Team))
	store.Must(Srv.Store.User().VerifyEmail(ruser.Data.(*model.User).Id))

	user2 := model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	ruser2, _ := Client.CreateUser(&user2, "")
	LinkUserToTeam(ruser2.Data.(*model.User), rteam.Data.(*model.Team))
	store.Must(Srv.Store.User().VerifyEmail(ruser2.Data.(*model.User).Id))

	team2 := model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	rteam2, _ := Client.CreateTeam(&team2)

	user3 := model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	ruser3, _ := Client.CreateUser(&user3, "")
	LinkUserToTeam(ruser3.Data.(*model.User), rteam2.Data.(*model.Team))
	store.Must(Srv.Store.User().VerifyEmail(ruser3.Data.(*model.User).Id))

	Client.Login(user.Email, user.Password)

	rId := ruser.Data.(*model.User).Id
	if result, err := Client.GetUser(rId, ""); err != nil {
		t.Fatal("Failed to get user")
	} else {
		if result.Data.(*model.User).Password != "" {
			t.Fatal("User shouldn't have any password data once set")
		}

		if cache_result, err := Client.GetUser(rId, result.Etag); err != nil {
			t.Fatal(err)
		} else if cache_result.Data.(*model.User) != nil {
			t.Fatal("cache should be empty")
		}
	}

	if result, err := Client.GetMe(""); err != nil {
		t.Fatal("Failed to get user")
	} else {
		if result.Data.(*model.User).Password != "" {
			t.Fatal("User shouldn't have any password data once set")
		}
	}

	if _, err := Client.GetUser("FORBIDDENERROR", ""); err == nil {
		t.Fatal("shouldn't exist")
	}

	if _, err := Client.GetUser(ruser2.Data.(*model.User).Id, ""); err == nil {
		t.Fatal("shouldn't have accss")
	}

	if userMap, err := Client.GetProfiles(rteam.Data.(*model.Team).Id, ""); err != nil {
		t.Fatal(err)
	} else if len(userMap.Data.(map[string]*model.User)) != 2 {
		t.Fatal("should have been 2")
	} else if userMap.Data.(map[string]*model.User)[rId].Id != rId {
		t.Fatal("should have been valid")
	} else {

		// test etag caching
		if cache_result, err := Client.GetProfiles(rteam.Data.(*model.Team).Id, userMap.Etag); err != nil {
			t.Fatal(err)
		} else if cache_result.Data.(map[string]*model.User) != nil {
			t.Log(cache_result.Data)
			t.Fatal("cache should be empty")
		}
	}

	if _, err := Client.GetProfiles(rteam2.Data.(*model.Team).Id, ""); err == nil {
		t.Fatal("shouldn't have access")
	}

	Client.AuthToken = ""
	if _, err := Client.GetUser(ruser2.Data.(*model.User).Id, ""); err == nil {
		t.Fatal("shouldn't have accss")
	}

	c := &Context{}
	c.RequestId = model.NewId()
	c.IpAddress = "cmd_line"
	UpdateUserRoles(c, ruser.Data.(*model.User), model.ROLE_SYSTEM_ADMIN)

	Client.Login(user.Email, "pwd")

	if _, err := Client.GetProfiles(rteam2.Data.(*model.Team).Id, ""); err != nil {
		t.Fatal(err)
	}
}

func TestGetDirectProfiles(t *testing.T) {
	th := Setup().InitBasic()

	th.BasicClient.Must(th.BasicClient.CreateDirectChannel(th.BasicUser2.Id))

	if result, err := th.BasicClient.GetDirectProfiles(""); err != nil {
		t.Fatal(err)
	} else {
		users := result.Data.(map[string]*model.User)

		if len(users) != 1 {
			t.Fatal("map was wrong length")
		}

		if users[th.BasicUser2.Id] == nil {
			t.Fatal("missing expected user")
		}
	}
}

func TestGetProfilesForDirectMessageList(t *testing.T) {
	th := Setup().InitBasic()

	th.BasicClient.Must(th.BasicClient.CreateDirectChannel(th.BasicUser2.Id))

	if result, err := th.BasicClient.GetProfilesForDirectMessageList(th.BasicTeam.Id); err != nil {
		t.Fatal(err)
	} else {
		users := result.Data.(map[string]*model.User)

		if len(users) < 1 {
			t.Fatal("map was wrong length")
		}
	}
}

func TestGetAudits(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	team := model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	rteam, _ := Client.CreateTeam(&team)

	user := model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	ruser, _ := Client.CreateUser(&user, "")
	LinkUserToTeam(ruser.Data.(*model.User), rteam.Data.(*model.Team))
	store.Must(Srv.Store.User().VerifyEmail(ruser.Data.(*model.User).Id))

	time.Sleep(100 * time.Millisecond)

	Client.Login(user.Email, user.Password)

	time.Sleep(100 * time.Millisecond)

	if result, err := Client.GetAudits(ruser.Data.(*model.User).Id, ""); err != nil {
		t.Fatal(err)
	} else {

		if len(result.Data.(model.Audits)) != 2 {
			t.Fatal(result.Data.(model.Audits))
		}

		if cache_result, err := Client.GetAudits(ruser.Data.(*model.User).Id, result.Etag); err != nil {
			t.Fatal(err)
		} else if cache_result.Data.(model.Audits) != nil {
			t.Fatal("cache should be empty")
		}
	}

	if _, err := Client.GetAudits("FORBIDDENERROR", ""); err == nil {
		t.Fatal("audit log shouldn't exist")
	}
}

func TestUserCreateImage(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	b, err := createProfileImage("Corey Hulen", "eo1zkdr96pdj98pjmq8zy35wba")
	if err != nil {
		t.Fatal(err)
	}

	rdr := bytes.NewReader(b)
	img, _, err2 := image.Decode(rdr)
	if err2 != nil {
		t.Fatal(err)
	}

	colorful := color.RGBA{116, 49, 196, 255}

	if img.At(1, 1) != colorful {
		t.Fatal("Failed to create correct color")
	}

	team := &model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	team = Client.Must(Client.CreateTeam(team)).Data.(*model.Team)

	user := &model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	user = Client.Must(Client.CreateUser(user, "")).Data.(*model.User)
	LinkUserToTeam(user, team)
	store.Must(Srv.Store.User().VerifyEmail(user.Id))

	Client.Login(user.Email, "pwd")

	Client.DoApiGet("/users/"+user.Id+"/image", "", "")

	if utils.Cfg.FileSettings.DriverName == model.IMAGE_DRIVER_S3 {
		var auth aws.Auth
		auth.AccessKey = utils.Cfg.FileSettings.AmazonS3AccessKeyId
		auth.SecretKey = utils.Cfg.FileSettings.AmazonS3SecretAccessKey

		s := s3.New(auth, aws.Regions[utils.Cfg.FileSettings.AmazonS3Region])
		bucket := s.Bucket(utils.Cfg.FileSettings.AmazonS3Bucket)

		if err := bucket.Del("/users/" + user.Id + "/profile.png"); err != nil {
			t.Fatal(err)
		}
	} else {
		path := utils.Cfg.FileSettings.Directory + "/users/" + user.Id + "/profile.png"
		if err := os.Remove(path); err != nil {
			t.Fatal("Couldn't remove file at " + path)
		}
	}
}

func TestUserUploadProfileImage(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	team := &model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	team = Client.Must(Client.CreateTeam(team)).Data.(*model.Team)

	user := &model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	user = Client.Must(Client.CreateUser(user, "")).Data.(*model.User)
	LinkUserToTeam(user, team)
	store.Must(Srv.Store.User().VerifyEmail(user.Id))

	if utils.Cfg.FileSettings.DriverName != "" {

		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)

		if _, upErr := Client.UploadProfileFile(body.Bytes(), writer.FormDataContentType()); upErr == nil {
			t.Fatal("Should have errored")
		}

		Client.Login(user.Email, "pwd")
		Client.SetTeamId(team.Id)

		if _, upErr := Client.UploadProfileFile(body.Bytes(), writer.FormDataContentType()); upErr == nil {
			t.Fatal("Should have errored")
		}

		part, err := writer.CreateFormFile("blargh", "test.png")
		if err != nil {
			t.Fatal(err)
		}

		path := utils.FindDir("tests")
		file, err := os.Open(path + "/test.png")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()

		_, err = io.Copy(part, file)
		if err != nil {
			t.Fatal(err)
		}

		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}

		if _, upErr := Client.UploadProfileFile(body.Bytes(), writer.FormDataContentType()); upErr == nil {
			t.Fatal("Should have errored")
		}

		file2, err := os.Open(path + "/test.png")
		if err != nil {
			t.Fatal(err)
		}
		defer file2.Close()

		body = &bytes.Buffer{}
		writer = multipart.NewWriter(body)

		part, err = writer.CreateFormFile("image", "test.png")
		if err != nil {
			t.Fatal(err)
		}

		if _, err := io.Copy(part, file2); err != nil {
			t.Fatal(err)
		}

		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}

		if _, upErr := Client.UploadProfileFile(body.Bytes(), writer.FormDataContentType()); upErr != nil {
			t.Fatal(upErr)
		}

		Client.DoApiGet("/users/"+user.Id+"/image", "", "")

		if utils.Cfg.FileSettings.DriverName == model.IMAGE_DRIVER_S3 {
			var auth aws.Auth
			auth.AccessKey = utils.Cfg.FileSettings.AmazonS3AccessKeyId
			auth.SecretKey = utils.Cfg.FileSettings.AmazonS3SecretAccessKey

			s := s3.New(auth, aws.Regions[utils.Cfg.FileSettings.AmazonS3Region])
			bucket := s.Bucket(utils.Cfg.FileSettings.AmazonS3Bucket)

			if err := bucket.Del("users/" + user.Id + "/profile.png"); err != nil {
				t.Fatal(err)
			}
		} else {
			path := utils.Cfg.FileSettings.Directory + "users/" + user.Id + "/profile.png"
			if err := os.Remove(path); err != nil {
				t.Fatal("Couldn't remove file at " + path)
			}
		}
	} else {
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		if _, upErr := Client.UploadProfileFile(body.Bytes(), writer.FormDataContentType()); upErr.StatusCode != http.StatusNotImplemented {
			t.Fatal("Should have failed with 501 - Not Implemented")
		}
	}
}

func TestUserUpdate(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	team := &model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	team = Client.Must(Client.CreateTeam(team)).Data.(*model.Team)

	time1 := model.GetMillis()

	user := &model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd", LastActivityAt: time1, LastPingAt: time1, Roles: ""}
	user = Client.Must(Client.CreateUser(user, "")).Data.(*model.User)
	LinkUserToTeam(user, team)
	store.Must(Srv.Store.User().VerifyEmail(user.Id))

	if _, err := Client.UpdateUser(user); err == nil {
		t.Fatal("Should have errored")
	}

	Client.Login(user.Email, "pwd")
	Client.SetTeamId(team.Id)

	time.Sleep(100 * time.Millisecond)

	time2 := model.GetMillis()

	time.Sleep(100 * time.Millisecond)

	user.Nickname = "Jim Jimmy"
	user.LastActivityAt = time2
	user.LastPingAt = time2
	user.Roles = model.ROLE_TEAM_ADMIN
	user.LastPasswordUpdate = 123

	if result, err := Client.UpdateUser(user); err != nil {
		t.Fatal(err)
	} else {
		if result.Data.(*model.User).Nickname != "Jim Jimmy" {
			t.Fatal("Nickname did not update properly")
		}
		if result.Data.(*model.User).LastActivityAt == time2 {
			t.Fatal("LastActivityAt should not have updated")
		}
		if result.Data.(*model.User).LastPingAt == time2 {
			t.Fatal("LastPingAt should not have updated")
		}
		if result.Data.(*model.User).Roles != "" {
			t.Fatal("Roles should not have updated")
		}
		if result.Data.(*model.User).LastPasswordUpdate == 123 {
			t.Fatal("LastPasswordUpdate should not have updated")
		}
	}

	user2 := &model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	user2 = Client.Must(Client.CreateUser(user2, "")).Data.(*model.User)
	LinkUserToTeam(user2, team)
	store.Must(Srv.Store.User().VerifyEmail(user2.Id))

	Client.Login(user2.Email, "pwd")
	Client.SetTeamId(team.Id)

	user.Nickname = "Tim Timmy"

	if _, err := Client.UpdateUser(user); err == nil {
		t.Fatal("Should have errored")
	}
}

func TestUserUpdatePassword(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	team := &model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	team = Client.Must(Client.CreateTeam(team)).Data.(*model.Team)
	Client.SetTeamId(team.Id)

	user := &model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	user = Client.Must(Client.CreateUser(user, "")).Data.(*model.User)
	LinkUserToTeam(user, team)
	store.Must(Srv.Store.User().VerifyEmail(user.Id))

	if _, err := Client.UpdateUserPassword(user.Id, "pwd", "newpwd"); err == nil {
		t.Fatal("Should have errored")
	}

	Client.Login(user.Email, "pwd")

	if _, err := Client.UpdateUserPassword("123", "pwd", "newpwd"); err == nil {
		t.Fatal("Should have errored")
	}

	if _, err := Client.UpdateUserPassword(user.Id, "", "newpwd"); err == nil {
		t.Fatal("Should have errored")
	}

	if _, err := Client.UpdateUserPassword(user.Id, "pwd", "npwd"); err == nil {
		t.Fatal("Should have errored")
	}

	if _, err := Client.UpdateUserPassword("12345678901234567890123456", "pwd", "newpwd"); err == nil {
		t.Fatal("Should have errored")
	}

	if _, err := Client.UpdateUserPassword(user.Id, "badpwd", "newpwd"); err == nil {
		t.Fatal("Should have errored")
	}

	if _, err := Client.UpdateUserPassword(user.Id, "pwd", "newpwd"); err != nil {
		t.Fatal(err)
	}

	updatedUser := Client.Must(Client.GetUser(user.Id, "")).Data.(*model.User)
	if updatedUser.LastPasswordUpdate == user.LastPasswordUpdate {
		t.Fatal("LastPasswordUpdate should have changed")
	}

	if _, err := Client.Login(user.Email, "newpwd"); err != nil {
		t.Fatal(err)
	}

	user2 := &model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	user2 = Client.Must(Client.CreateUser(user2, "")).Data.(*model.User)
	LinkUserToTeam(user2, team)

	Client.Login(user2.Email, "pwd")

	if _, err := Client.UpdateUserPassword(user.Id, "pwd", "newpwd"); err == nil {
		t.Fatal("Should have errored")
	}
}

func TestUserUpdateRoles(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	team := &model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	team = Client.Must(Client.CreateTeam(team)).Data.(*model.Team)

	user := &model.User{Email: "success+" + model.NewId() + "@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	user = Client.Must(Client.CreateUser(user, "")).Data.(*model.User)
	LinkUserToTeam(user, team)
	store.Must(Srv.Store.User().VerifyEmail(user.Id))

	user2 := &model.User{Email: "success+" + model.NewId() + "@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	user2 = Client.Must(Client.CreateUser(user2, "")).Data.(*model.User)
	LinkUserToTeam(user2, team)
	store.Must(Srv.Store.User().VerifyEmail(user2.Id))

	data := make(map[string]string)
	data["user_id"] = user.Id
	data["new_roles"] = ""

	if _, err := Client.UpdateUserRoles(data); err == nil {
		t.Fatal("Should have errored, not logged in")
	}

	Client.Login(user2.Email, "pwd")
	Client.SetTeamId(team.Id)

	if _, err := Client.UpdateUserRoles(data); err == nil {
		t.Fatal("Should have errored, not admin")
	}

	team2 := &model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	team2 = Client.Must(Client.CreateTeam(team2)).Data.(*model.Team)

	user3 := &model.User{Email: "success+" + model.NewId() + "@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	user3 = Client.Must(Client.CreateUser(user3, "")).Data.(*model.User)
	LinkUserToTeam(user3, team2)
	store.Must(Srv.Store.User().VerifyEmail(user3.Id))

	Client.Login(user3.Email, "pwd")
	Client.SetTeamId(team2.Id)

	data["user_id"] = user2.Id

	if _, err := Client.UpdateUserRoles(data); err == nil {
		t.Fatal("Should have errored, wrong team")
	}

	Client.Login(user.Email, "pwd")

	data["user_id"] = "junk"

	if _, err := Client.UpdateUserRoles(data); err == nil {
		t.Fatal("Should have errored, bad id")
	}

	data["user_id"] = "12345678901234567890123456"

	if _, err := Client.UpdateUserRoles(data); err == nil {
		t.Fatal("Should have errored, bad id")
	}

	data["user_id"] = user2.Id
	data["new_roles"] = "junk"

	if _, err := Client.UpdateUserRoles(data); err == nil {
		t.Fatal("Should have errored, bad role")
	}
}

func TestUserUpdateRolesMoreCases(t *testing.T) {
	th := Setup().InitSystemAdmin().InitBasic()

	data := make(map[string]string)

	// invalid team Id
	data["user_id"] = th.BasicUser2.Id
	data["new_roles"] = ""
	data["team_id"] = model.NewId()
	if _, err := th.BasicClient.UpdateUserRoles(data); err == nil {
		t.Fatal("Should have errored")
	}

	// user 1 is trying to change user 2
	data["user_id"] = th.BasicUser2.Id
	data["new_roles"] = ""
	data["team_id"] = th.BasicTeam.Id
	if _, err := th.BasicClient.UpdateUserRoles(data); err == nil {
		t.Fatal("Should have errored, you can only demote yourself")
	}

	// user 1 is trying to promote user 2
	data["user_id"] = th.BasicUser2.Id
	data["new_roles"] = model.ROLE_TEAM_ADMIN
	data["team_id"] = th.BasicTeam.Id
	if _, err := th.BasicClient.UpdateUserRoles(data); err == nil {
		t.Fatal("Should have errored, you can only demote yourself")
	}

	// user 1 is trying to promote user 2
	data["user_id"] = th.BasicUser2.Id
	data["new_roles"] = model.ROLE_SYSTEM_ADMIN
	data["team_id"] = th.BasicTeam.Id
	if _, err := th.BasicClient.UpdateUserRoles(data); err == nil {
		t.Fatal("Should have errored, you can only demote yourself")
	}

	// user 1 is trying to promote himself
	data["user_id"] = th.BasicUser.Id
	data["new_roles"] = model.ROLE_TEAM_ADMIN
	data["team_id"] = th.BasicTeam.Id
	if _, err := th.BasicClient.UpdateUserRoles(data); err == nil {
		t.Fatal("Should have errored, you cannot elevate your permissions")
	}

	// user 1 is trying to promote himself
	data["user_id"] = th.BasicUser.Id
	data["new_roles"] = model.ROLE_SYSTEM_ADMIN
	data["team_id"] = th.BasicTeam.Id
	if _, err := th.BasicClient.UpdateUserRoles(data); err == nil {
		t.Fatal("Should have errored, you cannot elevate your permissions")
	}

	th.LoginSystemAdmin()

	// promote user to team admin
	data["user_id"] = th.BasicUser.Id
	data["new_roles"] = model.ROLE_TEAM_ADMIN
	data["team_id"] = th.BasicTeam.Id
	if _, err := th.SystemAdminClient.UpdateUserRoles(data); err != nil {
		t.Fatal("Should have succeeded since they are system admin")
	}

	// demote team admin to basic member
	data["user_id"] = th.BasicUser.Id
	data["new_roles"] = ""
	data["team_id"] = th.BasicTeam.Id
	if _, err := th.SystemAdminClient.UpdateUserRoles(data); err != nil {
		t.Fatal("Should have succeeded since they are system admin")
	}

	// re-promote user to team admin
	data["user_id"] = th.BasicUser.Id
	data["new_roles"] = model.ROLE_TEAM_ADMIN
	data["team_id"] = th.BasicTeam.Id
	if _, err := th.SystemAdminClient.UpdateUserRoles(data); err != nil {
		t.Fatal("Should have succeeded since they are system admin")
	}

	// user 1 is promoting user 2 to team admin
	data["user_id"] = th.BasicUser2.Id
	data["new_roles"] = model.ROLE_TEAM_ADMIN
	data["team_id"] = th.BasicTeam.Id
	if _, err := th.BasicClient.UpdateUserRoles(data); err == nil {
		t.Fatal("Should have succeeded since they are team admin")
	}

	// user 1 is trying to promote user 2 from team admin to system admin
	data["user_id"] = th.BasicUser2.Id
	data["new_roles"] = model.ROLE_SYSTEM_ADMIN
	data["team_id"] = th.BasicTeam.Id
	if _, err := th.BasicClient.UpdateUserRoles(data); err == nil {
		t.Fatal("Should have errored, can only be system admin")
	}

	// user 1 is demoting user 2 to a regular member
	data["user_id"] = th.BasicUser2.Id
	data["new_roles"] = ""
	data["team_id"] = th.BasicTeam.Id
	if _, err := th.BasicClient.UpdateUserRoles(data); err == nil {
		t.Fatal("Should have succeeded since they are team admin")
	}

	// user 1 is trying to demote system admin
	data["user_id"] = th.SystemAdminUser.Id
	data["new_roles"] = ""
	data["team_id"] = th.BasicTeam.Id
	if _, err := th.BasicClient.UpdateUserRoles(data); err == nil {
		t.Fatal("Should have errored, can only be system admin")
	}

	// user 1 as team admin is demoting himself
	data["user_id"] = th.BasicUser.Id
	data["new_roles"] = ""
	data["team_id"] = th.BasicTeam.Id
	if _, err := th.BasicClient.UpdateUserRoles(data); err != nil {
		t.Fatal("Should have succeeded")
	}

	// system admin demoting himself
	data["user_id"] = th.SystemAdminUser.Id
	data["new_roles"] = ""
	data["team_id"] = ""
	if _, err := th.SystemAdminClient.UpdateUserRoles(data); err != nil {
		t.Fatal("Should have succeeded since they are system admin")
	}
}

func TestUserUpdateDeviceId(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	team := &model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	team = Client.Must(Client.CreateTeam(team)).Data.(*model.Team)

	user := &model.User{Email: "success+" + model.NewId() + "@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	user = Client.Must(Client.CreateUser(user, "")).Data.(*model.User)
	LinkUserToTeam(user, team)
	store.Must(Srv.Store.User().VerifyEmail(user.Id))

	Client.Login(user.Email, "pwd")
	Client.SetTeamId(team.Id)
	deviceId := model.PUSH_NOTIFY_APPLE + ":1234567890"

	if _, err := Client.AttachDeviceId(deviceId); err != nil {
		t.Fatal(err)
	}

	if result := <-Srv.Store.Session().GetSessions(user.Id); result.Err != nil {
		t.Fatal(result.Err)
	} else {
		sessions := result.Data.([]*model.Session)

		if sessions[0].DeviceId != deviceId {
			t.Fatal("Missing device Id")
		}
	}
}

func TestUserUpdateActive(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	team := &model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	team = Client.Must(Client.CreateTeam(team)).Data.(*model.Team)

	user := &model.User{Email: "success+" + model.NewId() + "@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	user = Client.Must(Client.CreateUser(user, "")).Data.(*model.User)
	LinkUserToTeam(user, team)
	store.Must(Srv.Store.User().VerifyEmail(user.Id))

	user2 := &model.User{Email: "success+" + model.NewId() + "@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	user2 = Client.Must(Client.CreateUser(user2, "")).Data.(*model.User)
	LinkUserToTeam(user2, team)
	store.Must(Srv.Store.User().VerifyEmail(user2.Id))

	if _, err := Client.UpdateActive(user.Id, false); err == nil {
		t.Fatal("Should have errored, not logged in")
	}

	Client.Login(user2.Email, "pwd")
	Client.SetTeamId(team.Id)

	if _, err := Client.UpdateActive(user.Id, false); err == nil {
		t.Fatal("Should have errored, not admin")
	}

	Client.Must(Client.Logout())

	team2 := &model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	team2 = Client.Must(Client.CreateTeam(team2)).Data.(*model.Team)

	user3 := &model.User{Email: "success+" + model.NewId() + "@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	user3 = Client.Must(Client.CreateUser(user3, "")).Data.(*model.User)
	LinkUserToTeam(user2, team2)
	store.Must(Srv.Store.User().VerifyEmail(user3.Id))

	Client.Login(user3.Email, "pwd")
	Client.SetTeamId(team2.Id)

	if _, err := Client.UpdateActive(user.Id, false); err == nil {
		t.Fatal("Should have errored, not yourself")
	}

	Client.Login(user.Email, "pwd")
	Client.SetTeamId(team.Id)

	if _, err := Client.UpdateActive("junk", false); err == nil {
		t.Fatal("Should have errored, bad id")
	}

	if _, err := Client.UpdateActive("12345678901234567890123456", false); err == nil {
		t.Fatal("Should have errored, bad id")
	}
}

func TestUserPermDelete(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	team := &model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	team = Client.Must(Client.CreateTeam(team)).Data.(*model.Team)

	user1 := &model.User{Email: model.NewId() + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	user1 = Client.Must(Client.CreateUser(user1, "")).Data.(*model.User)
	LinkUserToTeam(user1, team)
	store.Must(Srv.Store.User().VerifyEmail(user1.Id))

	Client.Login(user1.Email, "pwd")
	Client.SetTeamId(team.Id)

	channel1 := &model.Channel{DisplayName: "TestGetPosts", Name: "a" + model.NewId() + "a", Type: model.CHANNEL_OPEN, TeamId: team.Id}
	channel1 = Client.Must(Client.CreateChannel(channel1)).Data.(*model.Channel)

	post1 := &model.Post{ChannelId: channel1.Id, Message: "search for post1"}
	post1 = Client.Must(Client.CreatePost(post1)).Data.(*model.Post)

	post2 := &model.Post{ChannelId: channel1.Id, Message: "search for post2"}
	post2 = Client.Must(Client.CreatePost(post2)).Data.(*model.Post)

	post3 := &model.Post{ChannelId: channel1.Id, Message: "#hashtag search for post3"}
	post3 = Client.Must(Client.CreatePost(post3)).Data.(*model.Post)

	post4 := &model.Post{ChannelId: channel1.Id, Message: "hashtag for post4"}
	post4 = Client.Must(Client.CreatePost(post4)).Data.(*model.Post)

	c := &Context{}
	c.RequestId = model.NewId()
	c.IpAddress = "test"

	err := PermanentDeleteUser(c, user1)
	if err != nil {
		t.Fatal(err)
	}

	Client.ClearOAuthToken()
}

func TestSendPasswordReset(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	team := &model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	team = Client.Must(Client.CreateTeam(team)).Data.(*model.Team)

	user := &model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	user = Client.Must(Client.CreateUser(user, "")).Data.(*model.User)
	LinkUserToTeam(user, team)
	store.Must(Srv.Store.User().VerifyEmail(user.Id))

	if _, err := Client.SendPasswordReset(user.Email); err != nil {
		t.Fatal(err)
	}

	if _, err := Client.SendPasswordReset(""); err == nil {
		t.Fatal("Should have errored - no email")
	}

	if _, err := Client.SendPasswordReset("junk@junk.com"); err == nil {
		t.Fatal("Should have errored - bad email")
	}

	authData := model.NewId()
	user2 := &model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", AuthData: &authData, AuthService: "random"}
	user2 = Client.Must(Client.CreateUser(user2, "")).Data.(*model.User)
	LinkUserToTeam(user2, team)
	store.Must(Srv.Store.User().VerifyEmail(user2.Id))

	if _, err := Client.SendPasswordReset(user2.Email); err == nil {
		t.Fatal("should have errored - SSO user can't send reset password link")
	}
}

func TestResetPassword(t *testing.T) {
	th := Setup().InitSystemAdmin()
	Client := th.SystemAdminClient
	team := th.SystemAdminTeam

	user := &model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	user = Client.Must(Client.CreateUser(user, "")).Data.(*model.User)
	LinkUserToTeam(user, team)
	store.Must(Srv.Store.User().VerifyEmail(user.Id))

	Client.Must(Client.SendPasswordReset(user.Email))

	var recovery *model.PasswordRecovery
	if result := <-Srv.Store.PasswordRecovery().Get(user.Id); result.Err != nil {
		t.Fatal(result.Err)
	} else {
		recovery = result.Data.(*model.PasswordRecovery)
	}

	if _, err := Client.ResetPassword(recovery.Code, ""); err == nil {
		t.Fatal("Should have errored - no password")
	}

	if _, err := Client.ResetPassword(recovery.Code, "newp"); err == nil {
		t.Fatal("Should have errored - password too short")
	}

	if _, err := Client.ResetPassword("", "newpwd"); err == nil {
		t.Fatal("Should have errored - no code")
	}

	if _, err := Client.ResetPassword("junk", "newpwd"); err == nil {
		t.Fatal("Should have errored - bad code")
	}

	code := ""
	for i := 0; i < model.PASSWORD_RECOVERY_CODE_SIZE; i++ {
		code += "a"
	}
	if _, err := Client.ResetPassword(code, "newpwd"); err == nil {
		t.Fatal("Should have errored - bad code")
	}

	if _, err := Client.ResetPassword(recovery.Code, "newpwd"); err != nil {
		t.Fatal(err)
	}

	Client.Logout()
	Client.Must(Client.LoginById(user.Id, "newpwd"))
	Client.SetTeamId(team.Id)

	Client.Must(Client.SendPasswordReset(user.Email))

	if result := <-Srv.Store.PasswordRecovery().Get(user.Id); result.Err != nil {
		t.Fatal(result.Err)
	} else {
		recovery = result.Data.(*model.PasswordRecovery)
	}

	authData := model.NewId()
	if result := <-Srv.Store.User().UpdateAuthData(user.Id, "random", &authData, ""); result.Err != nil {
		t.Fatal(result.Err)
	}

	if _, err := Client.ResetPassword(recovery.Code, "newpwd"); err == nil {
		t.Fatal("Should have errored - sso user")
	}
}

func TestUserUpdateNotify(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	team := &model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	team = Client.Must(Client.CreateTeam(team)).Data.(*model.Team)

	user := &model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd", Roles: ""}
	user = Client.Must(Client.CreateUser(user, "")).Data.(*model.User)
	LinkUserToTeam(user, team)
	store.Must(Srv.Store.User().VerifyEmail(user.Id))

	data := make(map[string]string)
	data["user_id"] = user.Id
	data["email"] = "true"
	data["desktop"] = "all"
	data["desktop_sound"] = "false"

	if _, err := Client.UpdateUserNotify(data); err == nil {
		t.Fatal("Should have errored - not logged in")
	}

	Client.Login(user.Email, "pwd")
	Client.SetTeamId(team.Id)

	if result, err := Client.UpdateUserNotify(data); err != nil {
		t.Fatal(err)
	} else {
		if result.Data.(*model.User).NotifyProps["desktop"] != data["desktop"] {
			t.Fatal("NotifyProps did not update properly - desktop")
		}
		if result.Data.(*model.User).NotifyProps["desktop_sound"] != data["desktop_sound"] {
			t.Fatal("NotifyProps did not update properly - desktop_sound")
		}
		if result.Data.(*model.User).NotifyProps["email"] != data["email"] {
			t.Fatal("NotifyProps did not update properly - email")
		}
	}

	if _, err := Client.UpdateUserNotify(nil); err == nil {
		t.Fatal("Should have errored")
	}

	data["user_id"] = "junk"
	if _, err := Client.UpdateUserNotify(data); err == nil {
		t.Fatal("Should have errored - junk user id")
	}

	data["user_id"] = "12345678901234567890123456"
	if _, err := Client.UpdateUserNotify(data); err == nil {
		t.Fatal("Should have errored - bad user id")
	}

	data["user_id"] = user.Id
	data["desktop"] = ""
	if _, err := Client.UpdateUserNotify(data); err == nil {
		t.Fatal("Should have errored - empty desktop notify")
	}

	data["desktop"] = "all"
	data["desktop_sound"] = ""
	if _, err := Client.UpdateUserNotify(data); err == nil {
		t.Fatal("Should have errored - empty desktop sound")
	}

	data["desktop_sound"] = "false"
	data["email"] = ""
	if _, err := Client.UpdateUserNotify(data); err == nil {
		t.Fatal("Should have errored - empty email")
	}
}

func TestFuzzyUserCreate(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	team := model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	rteam, _ := Client.CreateTeam(&team)

	for i := 0; i < len(utils.FUZZY_STRINGS_NAMES) || i < len(utils.FUZZY_STRINGS_EMAILS); i++ {
		testName := "Name"
		testEmail := "test@nowhere.com"

		if i < len(utils.FUZZY_STRINGS_NAMES) {
			testName = utils.FUZZY_STRINGS_NAMES[i]
		}
		if i < len(utils.FUZZY_STRINGS_EMAILS) {
			testEmail = utils.FUZZY_STRINGS_EMAILS[i]
		}

		user := model.User{Email: strings.ToLower(model.NewId()) + testEmail, Nickname: testName, Password: "hello"}

		ruser, err := Client.CreateUser(&user, "")
		if err != nil {
			t.Fatal(err)
		}

		LinkUserToTeam(ruser.Data.(*model.User), rteam.Data.(*model.Team))
	}
}

func TestStatuses(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	team := model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	rteam, _ := Client.CreateTeam(&team)

	user := model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	ruser := Client.Must(Client.CreateUser(&user, "")).Data.(*model.User)
	LinkUserToTeam(ruser, rteam.Data.(*model.Team))
	store.Must(Srv.Store.User().VerifyEmail(ruser.Id))

	user2 := model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	ruser2 := Client.Must(Client.CreateUser(&user2, "")).Data.(*model.User)
	LinkUserToTeam(ruser2, rteam.Data.(*model.Team))
	store.Must(Srv.Store.User().VerifyEmail(ruser2.Id))

	Client.Login(user.Email, user.Password)
	Client.SetTeamId(team.Id)

	userIds := []string{ruser2.Id}

	r1, err := Client.GetStatuses(userIds)
	if err != nil {
		t.Fatal(err)
	}

	statuses := r1.Data.(map[string]string)

	if len(statuses) != 1 {
		t.Log(statuses)
		t.Fatal("invalid number of statuses")
	}

	for _, status := range statuses {
		if status != model.USER_OFFLINE && status != model.USER_AWAY && status != model.USER_ONLINE {
			t.Fatal("one of the statuses had an invalid value")
		}
	}

}

func TestEmailToOAuth(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	team := model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	rteam, _ := Client.CreateTeam(&team)

	user := model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	ruser := Client.Must(Client.CreateUser(&user, "")).Data.(*model.User)
	LinkUserToTeam(ruser, rteam.Data.(*model.Team))
	store.Must(Srv.Store.User().VerifyEmail(ruser.Id))

	m := map[string]string{}
	if _, err := Client.EmailToOAuth(m); err == nil {
		t.Fatal("should have failed - empty data")
	}

	m["password"] = "pwd"
	_, err := Client.EmailToOAuth(m)
	if err == nil {
		t.Fatal("should have failed - missing team_name, service, email")
	}

	m["team_name"] = team.Name
	if _, err := Client.EmailToOAuth(m); err == nil {
		t.Fatal("should have failed - missing service, email")
	}

	m["service"] = "someservice"
	if _, err := Client.EmailToOAuth(m); err == nil {
		t.Fatal("should have failed - missing email")
	}

	m["team_name"] = "junk"
	if _, err := Client.EmailToOAuth(m); err == nil {
		t.Fatal("should have failed - bad team name")
	}

	m["team_name"] = team.Name
	m["email"] = "junk"
	if _, err := Client.EmailToOAuth(m); err == nil {
		t.Fatal("should have failed - bad email")
	}

	m["email"] = ruser.Email
	m["password"] = "junk"
	if _, err := Client.EmailToOAuth(m); err == nil {
		t.Fatal("should have failed - bad password")
	}
}

func TestOAuthToEmail(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	team := model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	rteam, _ := Client.CreateTeam(&team)

	user := model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	ruser := Client.Must(Client.CreateUser(&user, "")).Data.(*model.User)
	LinkUserToTeam(ruser, rteam.Data.(*model.Team))
	store.Must(Srv.Store.User().VerifyEmail(ruser.Id))

	user2 := model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	ruser2 := Client.Must(Client.CreateUser(&user2, "")).Data.(*model.User)
	LinkUserToTeam(ruser2, rteam.Data.(*model.Team))
	store.Must(Srv.Store.User().VerifyEmail(ruser2.Id))

	m := map[string]string{}
	if _, err := Client.OAuthToEmail(m); err == nil {
		t.Fatal("should have failed - not logged in")
	}

	Client.Login(user.Email, user.Password)

	if _, err := Client.OAuthToEmail(m); err == nil {
		t.Fatal("should have failed - empty data")
	}

	m["password"] = "pwd"
	_, err := Client.OAuthToEmail(m)
	if err == nil {
		t.Fatal("should have failed - missing team_name, service, email")
	}

	m["team_name"] = team.Name
	if _, err := Client.OAuthToEmail(m); err == nil {
		t.Fatal("should have failed - missing email")
	}

	m["team_name"] = team.Name
	m["email"] = "junk"
	if _, err := Client.OAuthToEmail(m); err == nil {
		t.Fatal("should have failed - bad email")
	}

	m["email"] = ruser2.Email
	if _, err := Client.OAuthToEmail(m); err == nil {
		t.Fatal("should have failed - wrong user")
	}
}

func TestLDAPToEmail(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	team := model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	rteam, _ := Client.CreateTeam(&team)

	user := model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	ruser := Client.Must(Client.CreateUser(&user, "")).Data.(*model.User)
	LinkUserToTeam(ruser, rteam.Data.(*model.Team))
	store.Must(Srv.Store.User().VerifyEmail(ruser.Id))

	Client.Login(user.Email, user.Password)

	m := map[string]string{}
	if _, err := Client.LDAPToEmail(m); err == nil {
		t.Fatal("should have failed - empty data")
	}

	m["email_password"] = "pwd"
	_, err := Client.LDAPToEmail(m)
	if err == nil {
		t.Fatal("should have failed - missing team_name, ldap_password, email")
	}

	m["team_name"] = team.Name
	if _, err := Client.LDAPToEmail(m); err == nil {
		t.Fatal("should have failed - missing email, ldap_password")
	}

	m["ldap_password"] = "pwd"
	if _, err := Client.LDAPToEmail(m); err == nil {
		t.Fatal("should have failed - missing email")
	}

	m["email"] = ruser.Email
	m["team_name"] = "junk"
	if _, err := Client.LDAPToEmail(m); err == nil {
		t.Fatal("should have failed - bad team name")
	}

	m["team_name"] = team.Name
	m["email"] = "junk"
	if _, err := Client.LDAPToEmail(m); err == nil {
		t.Fatal("should have failed - bad email")
	}

	m["email"] = user.Email
	if _, err := Client.LDAPToEmail(m); err == nil {
		t.Fatal("should have failed - user is not an LDAP user")
	}
}

func TestEmailToLDAP(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	team := model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	rteam, _ := Client.CreateTeam(&team)

	user := model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	ruser := Client.Must(Client.CreateUser(&user, "")).Data.(*model.User)
	LinkUserToTeam(ruser, rteam.Data.(*model.Team))
	store.Must(Srv.Store.User().VerifyEmail(ruser.Id))

	Client.Login(user.Email, user.Password)

	m := map[string]string{}
	if _, err := Client.EmailToLDAP(m); err == nil {
		t.Fatal("should have failed - empty data")
	}

	m["email_password"] = "pwd"
	_, err := Client.EmailToLDAP(m)
	if err == nil {
		t.Fatal("should have failed - missing team_name, ldap_id, ldap_password, email")
	}

	m["team_name"] = team.Name
	if _, err := Client.EmailToLDAP(m); err == nil {
		t.Fatal("should have failed - missing email, ldap_password, ldap_id")
	}

	m["ldap_id"] = "someid"
	if _, err := Client.EmailToLDAP(m); err == nil {
		t.Fatal("should have failed - missing email, ldap_password")
	}

	m["ldap_password"] = "pwd"
	if _, err := Client.EmailToLDAP(m); err == nil {
		t.Fatal("should have failed - missing email")
	}

	m["email"] = ruser.Email
	m["team_name"] = "junk"
	if _, err := Client.EmailToLDAP(m); err == nil {
		t.Fatal("should have failed - bad team name")
	}

	m["team_name"] = team.Name
	m["email"] = "junk"
	if _, err := Client.EmailToLDAP(m); err == nil {
		t.Fatal("should have failed - bad email")
	}

	m["email"] = user.Email
	m["email_password"] = "junk"
	if _, err := Client.EmailToLDAP(m); err == nil {
		t.Fatal("should have failed - bad password")
	}

	m["email_password"] = "pwd"
	if _, err := Client.EmailToLDAP(m); err == nil {
		t.Fatal("should have failed - missing ldap bits or user")
	}
}

func TestMeInitialLoad(t *testing.T) {
	th := Setup().InitBasic()

	if result, err := th.BasicClient.GetInitialLoad(); err != nil {
		t.Fatal(err)
	} else {
		il := result.Data.(*model.InitialLoad)

		if il.User == nil {
			t.Fatal("should be valid")
		}

		if il.Preferences == nil {
			t.Fatal("should be valid")
		}

		if len(il.Teams) != 1 {
			t.Fatal("should be valid")
		}

		if len(il.TeamMembers) != 1 {
			t.Fatal("should be valid")
		}

		if len(il.ClientCfg) == 0 {
			t.Fatal("should be valid")
		}

		if len(il.LicenseCfg) == 0 {
			t.Fatal("should be valid")
		}
	}

	th.BasicClient.Logout()

	if result, err := th.BasicClient.GetInitialLoad(); err != nil {
		t.Fatal(err)
	} else {
		il := result.Data.(*model.InitialLoad)

		if il.User != nil {
			t.Fatal("should be valid")
		}

		if il.Preferences != nil {
			t.Fatal("should be valid")
		}

		if len(il.Teams) != 0 {
			t.Fatal("should be valid")
		}

		if len(il.TeamMembers) != 0 {
			t.Fatal("should be valid")
		}

		if len(il.ClientCfg) == 0 {
			t.Fatal("should be valid")
		}

		if len(il.LicenseCfg) == 0 {
			t.Fatal("should be valid")
		}
	}

}

func TestGenerateMfaQrCode(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	team := model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	rteam, _ := Client.CreateTeam(&team)

	user := model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	ruser, _ := Client.CreateUser(&user, "")
	LinkUserToTeam(ruser.Data.(*model.User), rteam.Data.(*model.Team))
	store.Must(Srv.Store.User().VerifyEmail(ruser.Data.(*model.User).Id))

	Client.Logout()

	if _, err := Client.GenerateMfaQrCode(); err == nil {
		t.Fatal("should have failed - not logged in")
	}

	Client.Login(user.Email, user.Password)

	if _, err := Client.GenerateMfaQrCode(); err == nil {
		t.Fatal("should have failed - not licensed")
	}

	// need to add more test cases when license and config can be configured for tests
}

func TestUpdateMfa(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	if utils.License.Features.MFA == nil {
		utils.License.Features.MFA = new(bool)
	}

	enableMfa := *utils.Cfg.ServiceSettings.EnableMultifactorAuthentication
	defer func() {
		utils.IsLicensed = false
		*utils.License.Features.MFA = false
		*utils.Cfg.ServiceSettings.EnableMultifactorAuthentication = enableMfa
	}()

	team := model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	rteam, _ := Client.CreateTeam(&team)

	user := model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	ruser, _ := Client.CreateUser(&user, "")
	LinkUserToTeam(ruser.Data.(*model.User), rteam.Data.(*model.Team))
	store.Must(Srv.Store.User().VerifyEmail(ruser.Data.(*model.User).Id))

	Client.Logout()

	if _, err := Client.UpdateMfa(true, "123456"); err == nil {
		t.Fatal("should have failed - not logged in")
	}

	Client.Login(user.Email, user.Password)

	if _, err := Client.UpdateMfa(true, ""); err == nil {
		t.Fatal("should have failed - no token")
	}

	if _, err := Client.UpdateMfa(true, "123456"); err == nil {
		t.Fatal("should have failed - not licensed")
	}

	utils.IsLicensed = true
	*utils.License.Features.MFA = true
	*utils.Cfg.ServiceSettings.EnableMultifactorAuthentication = true

	if _, err := Client.UpdateMfa(true, "123456"); err == nil {
		t.Fatal("should have failed - bad token")
	}

	// need to add more test cases when enterprise bits can be loaded into tests
}

func TestCheckMfa(t *testing.T) {
	th := Setup()
	Client := th.CreateClient()

	team := model.Team{DisplayName: "Name", Name: "z-z-" + model.NewId() + "a", Email: "test@nowhere.com", Type: model.TEAM_OPEN}
	rteam, _ := Client.CreateTeam(&team)

	user := model.User{Email: strings.ToLower(model.NewId()) + "success+test@simulator.amazonses.com", Nickname: "Corey Hulen", Password: "pwd"}
	ruser, _ := Client.CreateUser(&user, "")
	LinkUserToTeam(ruser.Data.(*model.User), rteam.Data.(*model.Team))
	store.Must(Srv.Store.User().VerifyEmail(ruser.Data.(*model.User).Id))

	if result, err := Client.CheckMfa(user.Email); err != nil {
		t.Fatal(err)
	} else {
		resp := result.Data.(map[string]string)
		if resp["mfa_required"] != "false" {
			t.Fatal("mfa should not be required")
		}
	}

	// need to add more test cases when enterprise bits can be loaded into tests
}
