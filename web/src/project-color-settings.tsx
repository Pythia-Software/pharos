import React, { useEffect, useState } from "react";
import { effectiveProjectColor, loadProjectColors, projectColors, saveProjectColor, useProjectColorRevision, type ProjectColor } from "./project-colors";
import { Icon } from "./icons";
import { useLocationRevision } from "./nested-pages";
import { projectColorSorts, sortProjectColors, type ProjectColorSort } from "./project-color-order";
import { useViewState } from "./view-state";
import { colorForeground } from "./model-color-math";
import "./project-colors.css";

const sortLabels: Record<ProjectColorSort, string> = { conversations: "Most conversations", latest: "Latest conversation", name: "Name" };
const latestLabel = (at?: string) => {
  const time = Date.parse(at ?? "");
  return Number.isFinite(time) ? `latest ${new Date(time).toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" })}` : "";
};

function ProjectColorRow({ project, projects, busy, save }: { project: ProjectColor; projects: ProjectColor[]; busy: boolean; save: (project: ProjectColor, color: string) => Promise<void> }) {
  const color = effectiveProjectColor(project);
  const [draft, setDraft] = useState(color);
  useEffect(() => { setDraft(color); }, [color]);
  const duplicate = projects.some(other => other.id !== project.id && effectiveProjectColor(other).toLowerCase() === draft.toLowerCase());
  return <div className="project-color-row">
    <div className="project-color-name"><strong>{project.name}</strong><small className="muted">{[project.kind === "repository" ? "Repository" : "Folder", project.conversations === undefined ? "" : `${project.conversations.toLocaleString()} ${project.conversations === 1 ? "conversation" : "conversations"}`, latestLabel(project.latest_conversation_at), project.custom_color ? "Your color" : project.source, duplicate ? "Shared with another project" : ""].filter(Boolean).join(" · ")}</small>{projects.some(other => other.id !== project.id && other.name === project.name) ? <small className="muted">{project.id}</small> : null}</div>
    <label className="project-color-picker" style={{ background: draft, color: colorForeground(draft) }}><input type="color" aria-label={`Color for ${project.name}`} value={draft} disabled={busy} onInput={event => setDraft(event.currentTarget.value)} onChange={event => setDraft(event.target.value)} /><Icon name="edit" /><span>Change color</span><code>{draft}</code></label>
    <div className="project-color-previews" aria-label="Color on light and dark backgrounds"><span className="project-color-light"><i style={{ background: draft }} />Light</span><span className="project-color-dark"><i style={{ background: draft }} />Dark</span></div>
    {!busy && draft !== color ? <button type="button" onClick={() => void save(project, draft)} aria-label={`Save color for ${project.name}`}>Save color</button> : null}
    {!busy && project.custom_color ? <button type="button" onClick={() => void save(project, "")} aria-label={`Restore default color for ${project.name}`}>Restore default</button> : null}
  </div>;
}

export function ProjectColorSettings() {
  useProjectColorRevision();
  useLocationRevision();
  const projects = projectColors();
  const [sort, setSort] = useViewState<ProjectColorSort>("project_color_sort", "conversations", location.pathname === "/settings/projects");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let active = true;
    void loadProjectColors().catch(error => { if (active) setError(error.message); });
    return () => { active = false; };
  }, []);
  async function save(project: ProjectColor, color: string) {
    setBusy(true); setError("");
    try { await saveProjectColor(project.id, color); }
    catch (error) { setError(error instanceof Error ? error.message : String(error)); }
    finally { setBusy(false); }
  }
  return <div className="project-color-settings">
    <h2>Project colors</h2>
    <p className="muted">Projects with more than 3 conversations keep one color across charts, cards, and dashboards, in light and dark themes. Smaller projects use fallback colors. Colors save with your library. Automatic colors use repository hints when available and keep apart from existing assignments.</p>
    {error ? <p role="alert">{error}</p> : null}
    {!projects.length ? <p className="muted">Projects appear here when they have more than 3 conversations.</p> : <>
      <label className="project-color-sort">Sort by <select data-view-field="project_color_sort" value={sort} onChange={event => setSort(event.target.value as ProjectColorSort)}>{projectColorSorts.map(option => <option key={option} value={option}>{sortLabels[option]}</option>)}</select></label>
      <div className="project-color-list">{sortProjectColors(projects, sort).map(project => <ProjectColorRow key={project.id} project={project} projects={projects} busy={busy} save={save} />)}</div>
    </>}
  </div>;
}
