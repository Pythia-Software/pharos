#!/usr/bin/env python3
"""Produce the exact Pythia Auth configuration proposal for this inbox."""
import json
import sys

origin = sys.argv[1].rstrip("/")
project = sys.argv[2] if len(sys.argv) > 2 else "ui-feedback"
json.dump({"version": 1, "title": "Set up the feedback inbox", "description": "Restrict the feedback inbox to grady@pythia.software.", "operations": [
    {"op": "project.create", "params": {"project_id": project, "display_name": "UI feedback inbox", "policy_mode": "allowlist", "role_schema": {"admin": ["*"]}, "default_role": "admin", "access_token_ttl_seconds": 900}},
    {"op": "project.member.set", "params": {"project_id": project, "email": "grady@pythia.software", "role": "admin"}},
    {"op": "project.redirect_uri.add", "params": {"project_id": project, "redirect_uri": origin+"/auth/callback"}},
]}, sys.stdout, indent=2)
sys.stdout.write("\n")
