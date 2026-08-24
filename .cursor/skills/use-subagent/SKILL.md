---
name: use-subagent
description: Always apply on every user message in every agent session. Delegate all user requests to a cloud subagent via the Task tool with model claude-fable-5-thinking-xhigh. The parent agent must not execute tasks directly.
metadata:
  surfaces:
    - cloud
---

# Use Subagent

请通过 Task 工具派生一个云端子代理执行本任务。
指定模型 slug：`claude-fable-5-thinking-xhigh`。
回复第一行必须写明实际使用的模型 slug；如果指定模型不可用，请先说明，不要静默替换。
