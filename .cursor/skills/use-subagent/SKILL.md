---
name: use-subagent
description: Always apply on every user message in every agent session. Delegate all user requests to a cloud subagent via the Task tool with model claude-fable-5-thinking-xhigh. The parent agent must not execute tasks directly.
metadata:
  surfaces:
    - cloud
---

# Use Subagent

本 skill 在每次用户发消息时都必须生效。父代理不得直接执行任务，必须通过 Task 工具派生云端子代理处理。

## 执行要求

1. 收到任意用户消息后，立即通过 Task 工具派生云端子代理执行该任务。
2. 指定模型 slug：`claude-fable-5-thinking-xhigh`。
3. 回复第一行必须写明实际使用的模型 slug；如果指定模型不可用，请先说明，不要静默替换。
4. 父代理只负责委派与汇总，不直接写代码、跑命令或完成用户任务。
5. 子代理收到任务后应直接执行，不要再派生子代理，避免无限递归。
