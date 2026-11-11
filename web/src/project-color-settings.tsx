import React, { useEffect, useState } from "react";
import { effectiveProjectColor, loadProjectColors, projectColors, saveProjectColor, type ProjectColor } from "./project-colors";
import "./project-colors.css";

function ProjectColorRow({ project, projects, busy, save }: { project: ProjectColor; projects: ProjectColor[]; busy: boolean; save: (project: ProjectColor, color: string) => Promise<void> }) {
  const color = effectiveProjectColor(project);
  const [draft, setDraft] = useState(color);
  useEffect(() => { setDraft(color); }, [color]);
  const duplicate = projects.some(other => other.id !== project.id && effectiveProjectColor(other).toLowerCase() === draft.toLowerCase());
  return <div className="project-color-row">
    <div className="project-color-name"><strong>{project.name}</strong><small className="muted">{project.kind === "repository" ? "Repository" : "Folder"} · {project.custom_color ? "Your color" : project.source}{duplicate ? " · Shared with another project" : ""}</small>{projects.some(other => other.id !== project.id && other.name === project.name) ? <small className="muted">{project.id}</small> : null}</div>
    <label className="project-color-picker"><input type="color" aria-label={`Color for ${project.name}`} value={draft} disabled={busy} onChange={event => setDraft(event.target.value)} /><code>{draft}</code></label>
    <div className="project-color-previews" aria-label="Color on light and dark backgrounds"><span className="project-color-light"><i style={{ background: draft }} />Light</span><span className="project-color-dark"><i style={{ background: draft }} />Dark</span></div>
    <button type="button" disabled={busy || draft === color} onClick={() => void save(project, draft)} aria-label={`Save color for ${project.name}`}>Save color</button>
    <button type="button" disabled={busy || !project.custom_color} onClick={() => void save(project, "")} aria-label={`Restore default color for ${project.name}`}>Restore default</button>
  </div>;
}

export function ProjectColorSettings() {
  const [projects, setProjects] = useState(projectColors);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let active = true;
    void loadProjectColors().then(projects => { if (active) setProjects(projects); }).catch(error => { if (active) setError(error.message); });
    return () => { active = false; };
  }, []);
  async function save(project: ProjectColor, color: string) {
    setBusy(true); setError("");
    try { setProjects(await saveProjectColor(project.id, color)); }
    catch (error) { setError(error instanceof Error ? error.message : String(error)); }
    finally { setBusy(false); }
  }
  return <div className="project-color-settings">
    <h2>Project colors</h2>
    <p className="muted">Each project keeps one color across charts, cards, and dashboards, in light and dark themes. Colors save with your library. Automatic colors use repository hints when available and keep apart from existing assignments.</p>
    {error ? <p role="alert">{error}</p> : null}
    {!projects.length ? <p className="muted">Projects appear here after their first workspace is indexed.</p> : <div className="project-color-list">{[...projects].sort((a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id)).map(project => <ProjectColorRow key={project.id} project={project} projects={projects} busy={busy} save={save} />)}</div>}
  </div>;
}
