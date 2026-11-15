import React from "react";
import { createRoot } from "react-dom/client";
import { DataTaxonomyModal } from "./data-taxonomy";
import "./view-attention.css";

// Setup can explain saved history before library pages or their APIs mount.
const host = document.createElement("div");
host.id = "dataGuideHost";
document.body.append(host);
createRoot(host).render(<DataTaxonomyModal />);
