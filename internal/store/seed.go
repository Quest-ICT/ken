package store

import "context"

// SeedDemo inserts (idempotently) one curated demo entry so the skeleton has
// something to return from kb_search / kb_get. Dev/smoke use only.
func (s *Store) SeedDemo(ctx context.Context) (string, error) {
	const slug = "docker-copy-manifests-before-source"

	var exists int
	_ = s.R.QueryRowContext(ctx, `SELECT 1 FROM entry WHERE slug = ?`, slug).Scan(&exists)
	if exists == 1 {
		return slug, nil
	}

	const (
		title   = "Copy dependency manifests before source, or every image rebuild reinstalls"
		summary = "A Dockerfile that copies the whole source before installing dependencies busts the dependency layer on every edit; copy the manifest + lockfile first."
		tags    = `["docker","build","caching"]`
		trig    = `["docker build reinstalls dependencies every time","npm install runs on every docker build","docker layer cache never hits","slow container builds"]`
		code    = `[{"lang":"dockerfile","caption":"manifests first, then source","snippet":"COPY package.json package-lock.json ./\nRUN npm ci\nCOPY . .\nRUN npm run build"}]`
	)

	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
INSERT INTO entry(slug,kind,title,summary,category,tags,triggers,lifecycle,staleness)
VALUES(?,?,?,?,?,?,?,'active','fresh')`,
		slug, "project", title, summary, "build", tags, trig)
	if err != nil {
		return "", err
	}
	entryID, _ := res.LastInsertId()

	res2, err := tx.ExecContext(ctx, `
INSERT INTO entry_version(entry_id,rev_no,state,title,summary,problem,solution,rationale,caveats,code,tags,triggers,author_kind,confidence,change_note)
VALUES(?,1,'curated',?,?,?,?,?,?,?,?,?,'human',0.98,'seed')`,
		entryID, title, summary,
		"A Dockerfile that runs COPY . . before the dependency install. Docker caches per instruction, keyed on the files that instruction touches, so touching any source file invalidates the COPY layer and every layer after it — including the install. Result: a one-line change re-downloads the whole dependency tree, and CI builds never get faster.",
		"Copy only the manifest and lockfile first, run the install, and copy the rest of the source afterwards: COPY package.json package-lock.json ./ then RUN npm ci then COPY . . — the same shape works for go.mod/go.sum, requirements.txt, Cargo.toml, pom.xml.",
		"Order the Dockerfile from least- to most-frequently-changing. Dependencies change rarely and cost the most to rebuild; source changes constantly and costs little. Trade-off: two COPY layers instead of one, and the manifest list has to be kept in step with the project.",
		"Copy the LOCKFILE too, not just the manifest — without it the install layer is not reproducible and can resolve differently on a cache miss. Also check .dockerignore: a file that lands in the build context (an .env, a local build dir, .git) still busts the cache even when nothing you edited matters.",
		code, tags, trig)
	if err != nil {
		return "", err
	}
	versionID, _ := res2.LastInsertId()

	if _, err := tx.ExecContext(ctx,
		`UPDATE entry SET curated_version_id = ?, curated_rev = 1 WHERE id = ?`, versionID, entryID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO curation_event(entry_id,version_id,event_type,to_state,actor_kind,note)
VALUES(?,?,'promoted','curated','human','seed')`, entryID, versionID); err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	return slug, nil
}
