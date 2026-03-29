# Bifrost Roadmap

## Pending (from codebase review)

### Medium priority
- [ ] `handleConversationSync` should update conversation metadata on sync (title, assignee changes)
- [ ] Federation E2E test should assert socket-side delivery to agent on receiving hub
- [ ] `ListEventsSince` timestamp cursor: add auto-increment rowid for reliable pagination
- [ ] Replace `time.Sleep` in tests with poll-with-timeout helpers
- [ ] `federation_routing_test.go` should start SyncEngine for meaningful assertions

### Low priority
- [ ] CLI should resolve agent IDs to display names in tasks, conversations, queue output
- [ ] `bifrost hub` should run as systemd service on remote hosts
- [ ] Plugin mode (`--plugin-dir`) MCP command resolution needs investigation
- [ ] Channel push E2E test blocked on claude.ai login requirement in CI
- [ ] Claude E2E tests flaky in CI (proxy connectivity/timeout)

## Future features
- [ ] Web UI dashboard (connected agents, messages, tasks)
- [ ] Agent capabilities registry ("I can run tests", "I can deploy")
- [ ] ACL for federation (per-agent permissions)
- [ ] Message history search
- [ ] Conversation groups / broadcast conversations
