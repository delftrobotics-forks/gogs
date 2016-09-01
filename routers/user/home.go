// Copyright 2014 The Gogs Authors. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package user

import (
	"bytes"
	"fmt"

	"github.com/Unknwon/com"
	"github.com/Unknwon/paginater"

	"github.com/gogits/gogs/models"
	"github.com/gogits/gogs/modules/base"
	"github.com/gogits/gogs/modules/context"
	"github.com/gogits/gogs/modules/setting"
)

const (
	DASHBOARD base.TplName = "user/dashboard/dashboard"
	ISSUES    base.TplName = "user/dashboard/issues"
	PROFILE   base.TplName = "user/profile"
	ORG_HOME  base.TplName = "org/home"
)

// getDashboardContextUser finds out dashboard is viewing as which context user.
func getDashboardContextUser(ctx *context.Context) *models.User {
	ctxUser := ctx.User
	orgName := ctx.Params(":org")
	if len(orgName) > 0 {
		// Organization.
		org, err := models.GetUserByName(orgName)
		if err != nil {
			if models.IsErrUserNotExist(err) {
				ctx.Handle(404, "GetUserByName", err)
			} else {
				ctx.Handle(500, "GetUserByName", err)
			}
			return nil
		}
		ctxUser = org
	}
	ctx.Data["ContextUser"] = ctxUser

	if err := ctx.User.GetOrganizations(true); err != nil {
		ctx.Handle(500, "GetOrganizations", err)
		return nil
	}
	ctx.Data["Orgs"] = ctx.User.Orgs

	return ctxUser
}

// retrieveFeeds loads feeds from database by given context user.
// The user could be organization so it is not always the logged in user,
// which is why we have to explicitly pass the context user ID.
func retrieveFeeds(ctx *context.Context, ctxUser *models.User, userID, offset int64, isProfile bool) {
	actions, err := models.GetFeeds(ctxUser, userID, offset, isProfile)
	if err != nil {
		ctx.Handle(500, "GetFeeds", err)
		return
	}

	// Check access of private repositories.
	feeds := make([]*models.Action, 0, len(actions))
	unameAvatars := make(map[string]string)
	for _, act := range actions {
		// Cache results to reduce queries.
		_, ok := unameAvatars[act.ActUserName]
		if !ok {
			u, err := models.GetUserByName(act.ActUserName)
			if err != nil {
				if models.IsErrUserNotExist(err) {
					continue
				}
				ctx.Handle(500, "GetUserByName", err)
				return
			}
			unameAvatars[act.ActUserName] = u.RelAvatarLink()
		}

		act.ActAvatar = unameAvatars[act.ActUserName]
		feeds = append(feeds, act)
	}
	ctx.Data["Feeds"] = feeds
}

func Dashboard(ctx *context.Context) {
	ctxUser := getDashboardContextUser(ctx)
	if ctx.Written() {
		return
	}

	ctx.Data["Title"] = ctxUser.DisplayName() + " - " + ctx.Tr("dashboard")
	ctx.Data["PageIsDashboard"] = true
	ctx.Data["PageIsNews"] = true

	// Only user can have collaborative repositories.
	if !ctxUser.IsOrganization() {
		collaborateRepos, err := ctx.User.GetAccessibleRepositories(setting.UI.User.RepoPagingNum)
		if err != nil {
			ctx.Handle(500, "GetAccessibleRepositories", err)
			return
		} else if err = models.RepositoryList(collaborateRepos).LoadAttributes(); err != nil {
			ctx.Handle(500, "RepositoryList.LoadAttributes", err)
			return
		}
		ctx.Data["CollaborativeRepos"] = collaborateRepos
	}

	var err error
	var repos, mirrors []*models.Repository
	if ctxUser.IsOrganization() {
		repos, _, err = ctxUser.GetUserRepositories(ctx.User.ID, 1, setting.UI.User.RepoPagingNum)
		if err != nil {
			ctx.Handle(500, "GetUserRepositories", err)
			return
		}

		mirrors, err = ctxUser.GetUserMirrorRepositories(ctx.User.ID)
		if err != nil {
			ctx.Handle(500, "GetUserMirrorRepositories", err)
			return
		}
	} else {
		if err = ctxUser.GetRepositories(1, setting.UI.User.RepoPagingNum); err != nil {
			ctx.Handle(500, "GetRepositories", err)
			return
		}
		repos = ctxUser.Repos

		mirrors, err = ctxUser.GetMirrorRepositories()
		if err != nil {
			ctx.Handle(500, "GetMirrorRepositories", err)
			return
		}
	}
	ctx.Data["Repos"] = repos
	ctx.Data["MaxShowRepoNum"] = setting.UI.User.RepoPagingNum

	if err := models.MirrorRepositoryList(mirrors).LoadAttributes(); err != nil {
		ctx.Handle(500, "MirrorRepositoryList.LoadAttributes", err)
		return
	}
	ctx.Data["MirrorCount"] = len(mirrors)
	ctx.Data["Mirrors"] = mirrors

	retrieveFeeds(ctx, ctxUser, ctx.User.ID, 0, false)
	if ctx.Written() {
		return
	}
	ctx.HTML(200, DASHBOARD)
}

func Issues(ctx *context.Context) {
	isPullList := ctx.Params(":type") == "pulls"
	if isPullList {
		ctx.Data["Title"] = ctx.Tr("pull_requests")
		ctx.Data["PageIsPulls"] = true
	} else {
		ctx.Data["Title"] = ctx.Tr("issues")
		ctx.Data["PageIsIssues"] = true
	}

	ctxUser := getDashboardContextUser(ctx)
	if ctx.Written() {
		return
	}

	// Organization does not have view type and filter mode.
	var (
		viewType   string
		sortType   = ctx.Query("sort")
		filterMode = models.FM_YOUR_REPOSITORIES
	)
	if ctxUser.IsOrganization() {
		viewType = "your_repositories"
	} else {
		viewType = ctx.Query("type")
		types := []string{"your_repositories", "assigned", "created_by"}
		if !com.IsSliceContainsStr(types, viewType) {
			viewType = "your_repositories"
		}

		switch viewType {
		case "your_repositories":
			filterMode = models.FM_YOUR_REPOSITORIES
		case "assigned":
			filterMode = models.FM_ASSIGN
		case "created_by":
			filterMode = models.FM_CREATE
		}
	}

	page := ctx.QueryInt("page")
	if page <= 1 {
		page = 1
	}

	repoID := ctx.QueryInt64("repo")
	isShowClosed := ctx.Query("state") == "closed"

	// Get repositories.
	var err error
	var repos []*models.Repository
	userRepoIDs := make([]int64, 0, len(repos))
	if ctxUser.IsOrganization() {
		repos, _, err = ctxUser.GetUserRepositories(ctx.User.ID, 1, ctxUser.NumRepos)
		if err != nil {
			ctx.Handle(500, "GetRepositories", err)
			return
		}
	} else {
		if err := ctxUser.GetRepositories(1, ctx.User.NumRepos); err != nil {
			ctx.Handle(500, "GetRepositories", err)
			return
		}
		repos = ctxUser.Repos
	}

	for _, repo := range repos {
		if (isPullList && repo.NumPulls == 0) ||
		(!isPullList &&
		(!repo.EnableIssues || repo.EnableExternalTracker || repo.NumIssues == 0)) {
			continue
		}

		userRepoIDs = append(userRepoIDs, repo.ID)
	}

	var issues []*models.Issue
	switch filterMode {
	case models.FM_YOUR_REPOSITORIES:
		// Get all issues from repositories from this user.
		issues, err = models.Issues(&models.IssuesOptions{
			RepoIDs:    userRepoIDs,
			RepoID:     repoID,
			Page:       page,
			IsClosed:   isShowClosed,
			IsPull:     isPullList,
			SortType:   sortType,
		})

	case models.FM_ASSIGN:
		// Get all issues assigned to this user.
		issues, err = models.Issues(&models.IssuesOptions{
			RepoID:     repoID,
			AssigneeID: ctxUser.ID,
			Page:       page,
			IsClosed:   isShowClosed,
			IsPull:     isPullList,
			SortType:   sortType,
		})

	case models.FM_CREATE:
		// Get all issues created by this user.
		issues, err = models.Issues(&models.IssuesOptions{
			RepoID:     repoID,
			PosterID:   ctxUser.ID,
			Page:       page,
			IsClosed:   isShowClosed,
			IsPull:     isPullList,
			SortType:   sortType,
		})
	}

	if err != nil {
		ctx.Handle(500, "Issues", err)
		return
	}

	showRepos    := make([]*models.Repository, 0, len(issues))
	showReposSet := make(map[int64]bool)

	if repoID > 0 {
		repo, err := models.GetRepositoryByID(repoID)
		if err != nil {
			ctx.Handle(500, "GetRepositoryByID", fmt.Errorf("[#%d]%v", repoID, err))
			return
		}

		if err = repo.GetOwner(); err != nil {
			ctx.Handle(500, "GetOwner", fmt.Errorf("[#%d]%v", repoID, err))
			return
		}

		showReposSet[repoID] = true
		showRepos            = append(showRepos, repo)
	}

	showIssues   := make([]*models.Issue, 0, len(issues))
	for _, issue := range issues {
		// Check if we want to show this issue.
		if (issue.IsClosed != isShowClosed) ||
		(repoID > 0 && repoID != issue.RepoID) {
			continue
		}

		// Get Repository data.
		issue.Repo, err = models.GetRepositoryByID(issue.RepoID)
		if err != nil {
			ctx.Handle(500, "GetRepositoryByID", fmt.Errorf("[#%d]%v", issue.RepoID, err))
			return
		}

		// Get Owner data.
		if err = issue.Repo.GetOwner(); err != nil {
			ctx.Handle(500, "GetOwner", fmt.Errorf("[#%d]%v", issue.RepoID, err))
			return
		}

		// Append to list of issues and repos.
		showIssues  = append(showIssues, issue)
		if filterMode == models.FM_YOUR_REPOSITORIES {
			// Use a map to make sure we don't add the same Repository twice.
			_, ok := showReposSet[issue.RepoID]
			if !ok {
				showReposSet[issue.RepoID] = true
				// Append to list of shown Repositories.
				showRepos                  = append(showRepos, issue.Repo)
			}
		}
	}

	issueStats := models.GetUserIssueStats(repoID, ctxUser.ID, userRepoIDs, filterMode, isPullList)

	ctx.Data["Issues"]       = showIssues
	ctx.Data["Repos"]        = showRepos
	ctx.Data["Page"]         = paginater.New(len(showIssues), setting.UI.IssuePagingNum, page, 5)
	ctx.Data["IssueStats"]   = issueStats
	ctx.Data["ViewType"]     = viewType
	ctx.Data["SortType"]     = sortType
	ctx.Data["RepoID"]       = repoID
	ctx.Data["IsShowClosed"] = isShowClosed

	if isShowClosed {
		ctx.Data["State"] = "closed"
	} else {
		ctx.Data["State"] = "open"
	}

	ctx.HTML(200, ISSUES)
}

func ShowSSHKeys(ctx *context.Context, uid int64) {
	keys, err := models.ListPublicKeys(uid)
	if err != nil {
		ctx.Handle(500, "ListPublicKeys", err)
		return
	}

	var buf bytes.Buffer
	for i := range keys {
		buf.WriteString(keys[i].OmitEmail())
		buf.WriteString("\n")
	}
	ctx.PlainText(200, buf.Bytes())
}

func showOrgProfile(ctx *context.Context) {
	ctx.SetParams(":org", ctx.Params(":username"))
	context.HandleOrgAssignment(ctx)
	if ctx.Written() {
		return
	}

	org := ctx.Org.Organization
	ctx.Data["Title"] = org.FullName

	page := ctx.QueryInt("page")
	if page <= 0 {
		page = 1
	}

	var (
		repos []*models.Repository
		count int64
		err   error
	)
	if ctx.IsSigned && !ctx.User.IsAdmin {
		repos, count, err = org.GetUserRepositories(ctx.User.ID, page, setting.UI.User.RepoPagingNum)
		if err != nil {
			ctx.Handle(500, "GetUserRepositories", err)
			return
		}
		ctx.Data["Repos"] = repos
	} else {
		showPrivate := ctx.IsSigned && ctx.User.IsAdmin
		repos, err = models.GetUserRepositories(org.ID, showPrivate, page, setting.UI.User.RepoPagingNum)
		if err != nil {
			ctx.Handle(500, "GetRepositories", err)
			return
		}
		ctx.Data["Repos"] = repos
		count = models.CountUserRepositories(org.ID, showPrivate)
	}
	ctx.Data["Page"] = paginater.New(int(count), setting.UI.User.RepoPagingNum, page, 5)

	if err := org.GetMembers(); err != nil {
		ctx.Handle(500, "GetMembers", err)
		return
	}
	ctx.Data["Members"] = org.Members

	ctx.Data["Teams"] = org.Teams

	ctx.HTML(200, ORG_HOME)
}

func Email2User(ctx *context.Context) {
	u, err := models.GetUserByEmail(ctx.Query("email"))
	if err != nil {
		if models.IsErrUserNotExist(err) {
			ctx.Handle(404, "GetUserByEmail", err)
		} else {
			ctx.Handle(500, "GetUserByEmail", err)
		}
		return
	}
	ctx.Redirect(setting.AppSubUrl + "/user/" + u.Name)
}
