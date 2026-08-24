---
name: use-subagent
description: Delegates tasks to cloud subagents via the Task tool with a specified model slug. Use when the user invokes /use-subagent or asks to run work through a cloud subagent.
disable-model-invocation: true
---

# Use Subagent

请通过 Task 工具派生一个云端子代理执行本任务。
指定模型 slug：`claude-fable-5-thinking-xhigh`。
回复第一行必须写明实际使用的模型 slug；如果指定模型不可用，请先说明，不要静默替换。
