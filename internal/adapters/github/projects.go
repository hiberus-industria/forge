package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/hiberus-industria/forge/internal/app"
	"github.com/hiberus-industria/forge/internal/domain"
)

// graphQL is a minimal GitHub GraphQL client; Projects v2 has no REST API.
type graphQL struct {
	hc  *http.Client
	url string
}

type gqlError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

func (g *graphQL) do(ctx context.Context, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.hc.Do(req)
	if err != nil {
		return fmt.Errorf("github graphql: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github graphql: unexpected status %s", resp.Status)
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("github graphql: decode: %w", err)
	}
	if len(envelope.Errors) > 0 {
		e := envelope.Errors[0]
		if e.Type == "NOT_FOUND" {
			return fmt.Errorf("github graphql: %s: %w", e.Message, app.ErrNotFound)
		}
		return fmt.Errorf("github graphql: %s", e.Message)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Data, out)
}

const repoProjectsQuery = `query($owner: String!, $name: String!) {
  repository(owner: $owner, name: $name) {
    projectsV2(first: 20) {
      nodes {
        id title url closed
        fields(first: 50) {
          nodes {
            ... on ProjectV2SingleSelectField { id name options { id name } }
          }
        }
      }
    }
  }
}`

func (c *Client) RepoProject(ctx context.Context, repo domain.RepoRef) (*app.ProjectInfo, error) {
	var data struct {
		Repository *struct {
			ProjectsV2 struct {
				Nodes []struct {
					ID     string `json:"id"`
					Title  string `json:"title"`
					URL    string `json:"url"`
					Closed bool   `json:"closed"`
					Fields struct {
						Nodes []struct {
							ID      string `json:"id"`
							Name    string `json:"name"`
							Options []struct {
								ID   string `json:"id"`
								Name string `json:"name"`
							} `json:"options"`
						} `json:"nodes"`
					} `json:"fields"`
				} `json:"nodes"`
			} `json:"projectsV2"`
		} `json:"repository"`
	}
	if err := c.gql.do(ctx, repoProjectsQuery, map[string]any{"owner": repo.Owner, "name": repo.Name}, &data); err != nil {
		return nil, err
	}
	if data.Repository == nil {
		return nil, fmt.Errorf("github: repository %s: %w", repo, app.ErrNotFound)
	}
	projects := data.Repository.ProjectsV2.Nodes
	sort.SliceStable(projects, func(i, j int) bool {
		return strings.ToLower(projects[i].Title) < strings.ToLower(projects[j].Title)
	})
	for _, p := range projects {
		if p.Closed {
			continue
		}
		for _, f := range p.Fields.Nodes {
			// Non single-select fields come back as empty objects.
			if f.ID == "" || !strings.EqualFold(f.Name, "status") {
				continue
			}
			info := &app.ProjectInfo{ID: p.ID, Title: p.Title, URL: p.URL, StatusFieldID: f.ID}
			for _, o := range f.Options {
				info.Options = append(info.Options, domain.StatusOption{ID: o.ID, Name: o.Name})
			}
			return info, nil
		}
	}
	return nil, nil
}

const addItemMutation = `mutation($project: ID!, $content: ID!) {
  addProjectV2ItemById(input: {projectId: $project, contentId: $content}) { item { id } }
}`

const setStatusMutation = `mutation($project: ID!, $item: ID!, $field: ID!, $option: String!) {
  updateProjectV2ItemFieldValue(input: {
    projectId: $project, itemId: $item, fieldId: $field,
    value: {singleSelectOptionId: $option}
  }) { projectV2Item { id } }
}`

func (c *Client) SetProjectStatus(ctx context.Context, sm domain.StatusMap, issue app.IssueInfo, state domain.TaskState) error {
	option := sm.Options[state]
	if option == "" || sm.ProjectID == "" || sm.StatusFieldID == "" {
		return nil
	}
	nodeID := issue.NodeID
	if nodeID == "" {
		full, err := c.GetIssue(ctx, issue.Ref)
		if err != nil {
			return err
		}
		nodeID = full.NodeID
	}
	// addProjectV2ItemById returns the existing item when already present.
	var added struct {
		AddProjectV2ItemByID struct {
			Item struct {
				ID string `json:"id"`
			} `json:"item"`
		} `json:"addProjectV2ItemById"`
	}
	if err := c.gql.do(ctx, addItemMutation, map[string]any{"project": sm.ProjectID, "content": nodeID}, &added); err != nil {
		return fmt.Errorf("add %s to project: %w", issue.Ref, err)
	}
	itemID := added.AddProjectV2ItemByID.Item.ID
	if itemID == "" {
		return fmt.Errorf("add %s to project: empty item id", issue.Ref)
	}
	vars := map[string]any{"project": sm.ProjectID, "item": itemID, "field": sm.StatusFieldID, "option": option}
	if err := c.gql.do(ctx, setStatusMutation, vars, nil); err != nil {
		return fmt.Errorf("set status of %s: %w", issue.Ref, err)
	}
	return nil
}
