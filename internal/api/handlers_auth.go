package api

import (
	"net/http"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/errors"
)

// loginRequest is the body of POST /auth/login.
type loginRequest struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	ClientName string `json:"client_name"`
	// SSHCode is the sign-in code of a user who turned on the SSH code
	// (SPEC §7.16); optional.
	SSHCode string `json:"ssh_code"`
}

// signupRequest is the body of POST /auth/signup.
type signupRequest struct {
	InvitationToken string `json:"invitation_token"`
	Username        string `json:"username"`
	DisplayName     string `json:"display_name"`
	Password        string `json:"password"`
	ClientName      string `json:"client_name"`
}

// tokenResponse is the answer of a login and of a sign-up.
type tokenResponse struct {
	Token     string   `json:"token"`
	ExpiresAt string   `json:"expires_at"`
	User      userJSON `json:"user"`
}

// invitationResponse is the answer of GET /auth/invitations/{token}.
type invitationResponse struct {
	Valid     bool   `json:"valid"`
	Role      string `json:"role"`
	ExpiresAt string `json:"expires_at"`
}

// authRoutes are the routes of login, sign-up and logout.
func (s *Server) authRoutes() []Route {
	return []Route{
		s.route(http.MethodPost, "/auth/login", true, s.login),
		s.route(http.MethodPost, "/auth/signup", true, s.signup),
		s.route(http.MethodGet, "/auth/invitations/{token}", true, s.invitation),
		s.route(http.MethodPost, "/auth/logout", false, s.logout),
	}
}

// login answers POST /auth/login: it starts a session of kind api and returns
// its token. A wrong username or password is 401, said the same way whether
// the user exists, and the login limiter answers 429 (SPEC D43, §7.1). For a
// user who turned on the SSH code (SPEC §7.16) the right password alone is 401
// ssh_code_required; with ssh_code too it uses the code up and logs in, and a
// wrong code is 401 unauthenticated and counts in the limiter.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in loginRequest
	if err := decodeJSON(w, r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	res, err := s.svc.Accounts.LoginClient(r.Context(), app.ClientLogin{
		Username:   in.Username,
		Password:   in.Password,
		ClientIP:   s.clientIP(r),
		Kind:       app.SessionAPI,
		ClientName: in.ClientName,
		SSHCode:    in.SSHCode,
	})
	if err != nil {
		if wrongCredentials(err) {
			s.writeProblem(w, r, unauthenticatedProblem(messageWrongCredentials))
			return
		}
		if isTag(err, app.TagUnauthenticated) {
			// The password was right and the code was not.
			s.writeProblem(w, r, unauthenticatedProblem(messageWrongCode))
			return
		}
		s.writeError(w, r, err)
		return
	}
	s.noteUser(r, res.User.ID)
	s.writeJSON(w, r, http.StatusOK, tokenOf(res))
}

// wrongCredentials reports whether err is the login's "wrong username or
// password", which the service reports as an invalid "form".
func wrongCredentials(err error) bool {
	appErr, ok := errors.FromError(err)
	if !ok || appErr.Tag != app.TagInvalidInput {
		return false
	}
	fields, _ := appErr.Metadata["fields"].(map[string]string)
	_, form := fields[app.FieldForm]
	return form
}

// tokenOf makes the answer of a login.
func tokenOf(res app.LoginResult) tokenResponse {
	return tokenResponse{Token: res.Token, ExpiresAt: rfc3339(res.ExpiresAt), User: userOf(res.User)}
}

// signup answers POST /auth/signup: it does what the sign-up page does, makes
// the user from the invitation, and then logs the new user in as login does.
// A used, expired or unknown invitation is 404 and counts as a failed login of
// the address of the client; past the limit of the login (5 in 15 minutes) the
// answer is 429 with Retry-After, before the invitation is looked at (SPEC D62).
func (s *Server) signup(w http.ResponseWriter, r *http.Request) {
	var in signupRequest
	if err := decodeJSON(w, r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	// The client name is checked before the user is made, so that a bad one
	// does not leave an account behind.
	if _, err := app.CleanClientName(app.SessionAPI, in.ClientName); err != nil {
		s.writeError(w, r, err)
		return
	}
	if _, err := s.svc.Accounts.SignUp(r.Context(), in.InvitationToken, in.Username, in.DisplayName, in.Password,
		app.FromAddress(s.clientIP(r))); err != nil {
		s.writeError(w, r, err)
		return
	}
	res, err := s.svc.Accounts.LoginClient(r.Context(), app.ClientLogin{
		Username:   in.Username,
		Password:   in.Password,
		ClientIP:   s.clientIP(r),
		Kind:       app.SessionAPI,
		ClientName: in.ClientName,
	})
	if err != nil {
		// The account exists; only the login after it failed. The client can
		// log in.
		s.logger.Warn("login after sign-up failed", "pattern", r.Pattern, "err", err)
		s.writeError(w, r, err)
		return
	}
	s.noteUser(r, res.User.ID)
	s.writeJSON(w, r, http.StatusCreated, tokenOf(res))
}

// invitation answers GET /auth/invitations/{token}: whether an invitation link
// can still be used, and the role it gives. A used, expired or unknown one is
// 404, and the answer does not say which.
func (s *Server) invitation(w http.ResponseWriter, r *http.Request) {
	info, err := s.svc.Accounts.InvitationDetails(r.Context(), r.PathValue("token"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, invitationResponse{Valid: true, Role: string(info.Role), ExpiresAt: rfc3339(info.ExpiresAt)})
}

// logout answers POST /auth/logout: it deletes the session of the token.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	if err := s.svc.Accounts.EndSession(r.Context(), c.actor, c.sessionID); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeNoContent(w)
}
