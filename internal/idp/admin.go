package idp

import (
	"context"
	"errors"
	"net/http"
	"time"
)

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+`
		FROM users u JOIN account_states a ON a.user_id = u.id ORDER BY u.created_at, u.rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

// SuspendUser revokes account access before network cleanup. A failed cleanup
// leaves a disabled account which the administrator can safely retry deleting.
func (s *Store) SuspendUser(ctx context.Context, actorID, userID string) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	actor, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+`
		FROM users u JOIN account_states a ON a.user_id = u.id WHERE u.id = ?`, actorID))
	if err != nil || !actor.IsAdmin || actor.Disabled {
		return User{}, ErrForbidden
	}
	if actorID == userID {
		return User{}, ErrSelfDelete
	}
	user, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+`
		FROM users u JOIN account_states a ON a.user_id = u.id WHERE u.id = ?`, userID))
	if err != nil {
		return User{}, err
	}
	if user.IsAdmin {
		var admins int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM account_states WHERE is_admin = 1 AND disabled = 0").Scan(&admins); err != nil {
			return User{}, err
		}
		if admins <= 1 {
			return User{}, ErrLastAdmin
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE account_states SET disabled = 1 WHERE user_id = ?", userID); err != nil {
		return User{}, err
	}
	for _, table := range []string{"sessions", "auth_codes", "access_tokens", "oauth_states"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE user_id = ?", userID); err != nil {
			return User{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	user.Disabled = true
	return user, nil
}

func (s *Store) DeleteSuspendedUser(ctx context.Context, userID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ? AND EXISTS
		(SELECT 1 FROM account_states WHERE user_id = users.id AND disabled = 1)`, userID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) (User, bool) {
	user, ok := s.currentUser(r)
	if !ok {
		http.Redirect(w, r, "/idp/login?return_to=%2Fidp%2Fusers", http.StatusSeeOther)
		return User{}, false
	}
	if !user.IsAdmin {
		s.renderMessage(w, r, http.StatusForbidden, "users.title", "users.forbidden")
		return User{}, false
	}
	return user, true
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		s.opts.Logger.Error("list registered accounts", "error", err)
		s.renderMessage(w, r, http.StatusInternalServerError, "users.title", "users.load_failed")
		return
	}
	data := pageData{TitleKey: "users.title", User: &user, Users: users, AdminPath: s.opts.AdminPath}
	if r.URL.Query().Get("deleted") == "1" {
		data.NoticeKey = "users.deleted"
	}
	s.render(w, r, "users", data)
}

func (s *Server) handleDeleteUserPage(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	target, err := s.store.UserByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.renderMessage(w, r, http.StatusNotFound, "users.title", "users.not_found")
		return
	}
	if target.ID == actor.ID {
		s.renderMessage(w, r, http.StatusConflict, "users.title", "users.self_delete")
		return
	}
	s.render(w, r, "delete-user", pageData{TitleKey: "users.delete", User: &actor, ManagedUser: &target, CSRF: s.ensureCSRF(w, r)})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil || !s.validateCSRF(r) {
		s.renderMessage(w, r, http.StatusForbidden, "users.delete", "err.csrf_expired")
		return
	}
	target, err := s.store.UserByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.renderMessage(w, r, http.StatusNotFound, "users.delete", "users.not_found")
		return
	}
	if normalizeEmail(r.FormValue("confirm_email")) != target.Email {
		s.renderStatus(w, r, http.StatusBadRequest, "delete-user", pageData{TitleKey: "users.delete", User: &actor, ManagedUser: &target, CSRF: s.ensureCSRF(w, r), ErrorKey: "users.confirm_mismatch"})
		return
	}
	if s.opts.DeleteNetwork == nil {
		s.renderMessage(w, r, http.StatusServiceUnavailable, "users.delete", "users.cleanup_failed")
		return
	}
	target, err = s.store.SuspendUser(r.Context(), actor.ID, target.ID)
	if err != nil {
		key := "users.delete_failed"
		if errors.Is(err, ErrSelfDelete) || errors.Is(err, ErrLastAdmin) {
			key = "users.self_delete"
		}
		s.renderMessage(w, r, http.StatusConflict, "users.delete", key)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if err := s.opts.DeleteNetwork(ctx, target.ID); err != nil {
		s.opts.Logger.Error("delete account network", "user", target.ID, "error", err)
		s.renderMessage(w, r, http.StatusServiceUnavailable, "users.delete", "users.cleanup_failed")
		return
	}
	if err := s.store.DeleteSuspendedUser(ctx, target.ID); err != nil {
		s.opts.Logger.Error("purge disabled account", "user", target.ID, "error", err)
		s.renderMessage(w, r, http.StatusInternalServerError, "users.delete", "users.delete_failed")
		return
	}
	s.opts.Logger.Info("registered account deleted", "administrator", actor.ID, "user", target.ID)
	http.Redirect(w, r, "/idp/users?deleted=1", http.StatusSeeOther)
}

// CurrentUser is the account boundary for the public Headplane proxy. It is
// checked on every request, including requests with an existing Headplane cookie.
func (s *Server) CurrentUser(r *http.Request) (User, bool) { return s.currentUser(r) }
