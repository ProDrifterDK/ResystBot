---
name: git-master
description: Git operations expert for commits, rebase, and history search
version: "1.0.0"
triggers:
  keywords: ["commit", "rebase", "git", "merge", "blame", "bisect"]
  tools: ["shell"]
  agents: ["default"]
inject:
  method: "system_prompt"
  priority: "normal"
  token_budget: 2000
author: "ProDrifterDK"
tags: ["git", "version-control"]
---

# Git Master Skill

Expert git operations assistant.
