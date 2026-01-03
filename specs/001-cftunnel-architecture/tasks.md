# Tasks: Cloudflare Tunnel Docker Controller

**Input**: Design documents from `/specs/001-cftunnel-architecture/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/

**Tests**: This project includes comprehensive testing tasks aligned with constitution requirements (unit, integration, contract tests per Quality Standards).

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (e.g., US1, US2, US3)
- Include exact file paths in descriptions

## Path Conventions

- **Go project structure**: `cmd/`, `internal/`, `pkg/`, `tests/` at repository root
- Paths shown below follow the plan.md structure exactly

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Project initialization and basic structure

- [X] T001 Create Go module and project directory structure per plan.md (cmd/, internal/, pkg/, tests/)
- [X] T002 Initialize go.mod with Go 1.24 and add required dependencies (docker/docker, cloudflare-go, viper, testify)
- [X] T003 [P] Create Makefile with build, test, fmt, clean, run targets per quickstart.md
- [X] T004 [P] Create config.yaml template with all configuration sections (log, cloudflare, controller, cleanup)
- [X] T005 [P] Setup gitignore for Go build artifacts, state files, and configuration
- [X] T006 [P] Initialize basic README.md with project overview and quickstart reference

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Core infrastructure that MUST be complete before ANY user story can be implemented

**⚠️ CRITICAL**: No user story work can begin until this phase is complete

- [X] T007 Implement structured logging wrapper in internal/logger/logger.go (log/slog integration, text/JSON formats)
- [X] T008 Create configuration loading in internal/config/config.go (Viper integration, YAML + env vars, validation)
- [X] T009 [P] Define public types in pkg/types/tunnel.go (TunnelEntry, IngressRule, OriginRequestConfig, RetentionPolicy, etc.)
- [X] T010 [P] Define event structures in internal/events/event.go (ContainerEvent, EventType enums)
- [X] T011 Create base configuration structures in internal/config/config.go (Config, CloudflareConfig, ControllerConfig, CleanupConfig)
- [X] T012 [P] Create unit tests for configuration loading in internal/config/config_test.go
- [X] T013 [P] Create unit tests for logging wrapper in internal/logger/logger_test.go

**Checkpoint**: Foundation ready - user story implementation can now begin in parallel

---

## Phase 3: User Story 1 - Automatic Container Exposure (Priority: P1) 🎯 MVP

**Goal**: Enable Docker containers with labels to be automatically exposed through Cloudflare Tunnel within 5 seconds

**Independent Test**: Start a labeled container (`docktunnel.enable=true`, `docktunnel.web.hostname=app.example.com`) and verify it's accessible via Cloudflare Tunnel within 5 seconds

### Unit Tests for User Story 1

- [X] T014 [P] [US1] Create mock Cloudflare client in tests/mocks/cloudflare_api.go (interface with GetTunnelConfig, UpdateTunnelConfig methods)
- [X] T015 [P] [US1] Create mock Docker client in tests/mocks/docker_client.go (interface with Events, ContainerInspect methods)
- [X] T016 [P] [US1] Write unit test for hostname uniqueness validation in internal/controller/validator_test.go

### Implementation for User Story 1

- [X] T017 [P] [US1] Implement Docker event stream monitoring in internal/docker/monitor.go (filter by docktunnel.enable=true label)
- [X] T018 [P] [US1] Implement Docker API client wrapper in internal/docker/client.go (ContainerInspect, ContainerList methods)
- [X] T019 [P] [US1] Implement basic label parser in internal/controller/label_parser.go (parse docktunnel.enable, docktunnel.<name>.hostname, docktunnel.<name>.service)
- [X] T020 [P] [US1] Implement validator in internal/controller/validator.go (hostname uniqueness, required fields validation)
- [X] T021 [P] [US1] Implement Cloudflare API client wrapper in internal/cloudflareManager/tunnel.go (GetTunnelConfig, UpdateTunnelConfig, FindTunnelByName)
- [X] T022 [P] [US1] Implement Cloudflare API rate limiter in internal/cloudflareManager/rate_limiter.go (token bucket, 10 req/s)
- [X] T023 [P] [US1] Implement retry logic with exponential backoff in internal/cloudflareManager/retry.go (max 3 retries, jitter)
- [X] T024 [P] [US1] Implement in-memory state manager in internal/state/manager.go (active tunnels map, add/remove operations)
- [X] T025 [US1] Implement controller event orchestration in internal/controller/controller.go (handle start/stop events, trigger Cloudflare sync)
- [X] T026 [US1] Implement Cloudflare tunnel configuration sync in internal/cloudflareManager/tunnel.go (fetch config, calculate diff, apply updates)
- [X] T027 [US1] Implement debouncer in internal/events/dispatcher.go (2-second window to batch rapid events)
- [X] T028 [US1] Create main entry point in cmd/docktunnel/main.go (initialization sequence, signal handling setup)
- [X] T029 [US1] Implement graceful shutdown in cmd/docktunnel/main.go (SIGTERM/SIGINT handling, complete in-flight operations)
- [X] T030 [US1] Add comprehensive logging at INFO/ERROR levels throughout controller and Cloudflare manager

**Checkpoint**: At this point, User Story 1 should be fully functional - labeled containers are automatically exposed via Cloudflare Tunnel

---

## Phase 4: User Story 4 - Configuration Priority Fallback (Priority: P1)

**Goal**: Automatically detect missing configuration values (ports, IPs, protocols) from lower-priority sources

**Independent Test**: Start container with only hostname label, verify system auto-detects port, IP, and constructs service URL

### Unit Tests for User Story 4

- [X] T031 [P] [US4] Write unit test for 4-layer priority parsing in internal/controller/label_parser_test.go (DockTunnel → Traefik → Auto-detect → Defaults)
- [X] T032 [P] [US4] Write unit test for IP address detection in internal/controller/label_parser_test.go (host vs bridge networking)
- [X] T033 [P] [US4] Write unit test for port auto-detection in internal/controller/label_parser_test.go (exposed ports, Traefik labels)
- [X] T034 [P] [US4] Write unit test for service URL construction in internal/controller/label_parser_test.go (protocol, IP, port combination)

### Implementation for User Story 4

- [X] T035 [US4] Extend label parser with Traefik compatibility in internal/controller/label_parser.go (parse traefik.http.routers.*.rule for Host() patterns)
- [X] T036 [US4] Implement Traefik hostname extraction in internal/controller/label_parser.go (regex parsing of Host(`example.com`) patterns)
- [X] T037 [US4] Implement Traefik service port extraction in internal/controller/label_parser.go (parse traefik.http.services.*.loadbalancer.server.port)
- [X] T038 [US4] Implement network mode detection in internal/controller/label_parser.go (host vs bridge, detect from ContainerJSON)
- [X] T039 [US4] Implement IP address auto-detection in internal/controller/label_parser.go (localhost for host networking, bridge IP for bridge)
- [X] T040 [US4] Implement port auto-detection in internal/controller/label_parser.go (first exposed Docker port, fallback to port 80)
- [X] T041 [US4] Implement protocol/scheme detection in internal/controller/label_parser.go (detect from labels, default to http)
- [X] T042 [US4] Implement service URL construction in internal/controller/label_parser.go (combine scheme, IP, port into http://ip:port format)
- [X] T043 [US4] Implement global defaults application in internal/config/config.go (apply defaults from config.yaml when labels missing)
- [X] T044 [US4] Add logging for configuration fallback decisions in internal/controller/label_parser.go (log which priority layer provided each value)

**Checkpoint**: At this point, User Stories 1 AND 4 should both work - containers can be configured with minimal labels and system auto-detects missing values

---

## Phase 5: User Story 2 - Traefik Compatibility Layer (Priority: P2)

**Goal**: Enable Traefik users to migrate without rewriting labels - parse standard Traefik v2 labels as fallback

**Independent Test**: Deploy container with only Traefik labels (no DockTunnel labels), verify correct tunnel configuration is generated

### Unit Tests for User Story 2

- [X] T045 [P] [US2] Write unit test for Traefik Host() regex extraction in internal/controller/label_parser_test.go (single and multiple hostnames)
- [X] T046 [P] [US2] Write unit test for Traefik Path/PathPrefix extraction in internal/controller/label_parser_test.go
- [X] T047 [P] [US2] Write unit test for Traefik service name linking in internal/controller/label_parser_test.go (router references service by name)
- [X] T048 [P] [US2] Write unit test for DockTunnel label precedence over Traefik in internal/controller/label_parser_test.go

### Implementation for User Story 2

- [X] T049 [US2] Implement Traefik router rule parser in internal/controller/label_parser.go (parse traefik.http.routers.<name>.rule with regex for Host() and Path())
- [X] T050 [US2] Implement Traefik service config parser in internal/controller/label_parser.go (parse traefik.http.services.<name>.loadbalancer.server.* fields)
- [X] T051 [US2] Implement service name matching logic in internal/controller/label_parser.go (link router service name to service configuration)
- [X] T052 [US2] Add support for extracting multiple hostnames from single Traefik rule in internal/controller/label_parser.go (Host(`a.com`, `b.com`) → multiple routes)
- [X] T053 [US2] Add logging for unsupported Traefik features at INFO level in internal/controller/label_parser.go (middleware, TLS configs not supported)
- [X] T054 [US2] Extend 4-layer priority resolver to prioritize DockTunnel labels over Traefik in internal/controller/label_parser.go

**Checkpoint**: At this point, User Stories 1, 2, AND 4 should all work - Traefik users can migrate without label changes

---

## Phase 6: User Story 3 - Configuration Retention Policies (Priority: P2)

**Goal**: Support immediate, timed, and forever retention policies for stopped containers' tunnel routes

**Independent Test**: Stop container with retention=30m, verify route persists for 30 minutes then auto-deletes

### Unit Tests for User Story 3

- [X] T055 [P] [US3] Write unit test for retention policy parsing in internal/controller/label_parser_test.go (immediate, timed, forever formats)
- [X] T056 [P] [US3] Write unit test for garbage collection logic in internal/state/manager_test.go (expire timed entries, preserve forever entries)
- [X] T057 [P] [US3] Write unit test for container restart canceling retention timer in internal/state/manager_test.go

### Implementation for User Story 3

- [X] T058 [P] [US3] Extend RetentionPolicy type with PolicyType enum in pkg/types/tunnel.go (Immediate, Timed, Forever)
- [X] T059 [P] [US3] Add retention policy parsing to label parser in internal/controller/label_parser.go (parse "0"/"immediate", "30m"/"1h", "forever"/"keep")
- [X] T060 [US3] Extend TunnelEntry with DeletedAt timestamp field in pkg/types/tunnel.go
- [X] T061 [US3] Implement container stop handler with retention logic in internal/controller/controller.go (mark as PENDING_DELETE or DELETED based on policy)
- [X] T062 [US3] Implement garbage collection ticker in internal/state/manager.go (run every 60 seconds)
- [X] T063 [US3] Implement GC scan logic in internal/state/manager.go (find expired PENDING_DELETE entries, remove from Cloudflare)
- [X] T064 [US3] Implement container restart detection during retention in internal/controller/controller.go (cancel retention timer, restore ACTIVE status)
- [X] T065 [US3] Update Cloudflare sync to exclude pending deletion entries from ingress rules in internal/cloudflareManager/tunnel.go (keep route active until retention expires)

**Checkpoint**: At this point, User Stories 1, 2, 3, AND 4 should all work - retention policies prevent config churn during deployments

---

## Phase 7: User Story 5 - State Persistence and Recovery (Priority: P3)

**Goal**: Persist controller state to disk and recover on restart to preserve retention timers

**Independent Test**: Stop container with retention=30m, restart controller after 10m, verify remaining 20m honored

### Unit Tests for User Story 5

- [X] T066 [P] [US5] Write unit test for state snapshot serialization in internal/state/persistence_test.go (gob encoding)
- [X] T067 [P] [US5] Write unit test for state snapshot deserialization in internal/state/persistence_test.go (handle corrupted files)
- [X] T068 [P] [US5] Write unit test for startup reconciliation with persisted state in internal/controller/controller_test.go

### Implementation for User Story 5

- [X] T069 [P] [US5] Define StateSnapshot structure in pkg/types/tunnel.go (active tunnels map, pending deletions map, flapping state, version, timestamp)
- [X] T070 [US5] Implement state persistence to file in internal/state/persistence.go (gob encoding, atomic write with tmp file + rename)
- [X] T071 [US5] Implement state loading from file in internal/state/persistence.go (gob decode, fallback to JSON, handle corruption)
- [X] T072 [US5] Add periodic state snapshot saves in internal/state/manager.go (save after every state change or every 30s)
- [X] T073 [US5] Implement startup state loading in cmd/docktunnel/main.go (load persisted state before event processing)
- [X] T074 [US5] Implement startup container scan reconciliation in internal/controller/controller.go (scan running containers, reconcile with persisted state, detect containers started during downtime)
- [X] T075 [US5] Add error handling for corrupted state file in internal/state/persistence.go (log error, fall back to full container scan, don't fail startup)

**Checkpoint**: At this point, all user stories except US6 should work - controller can recover from restarts without losing retention state

---

## Phase 8: User Story 6 - Advanced Origin Request Configuration (Priority: P3)

**Goal**: Support fine-grained control over TLS, timeouts, HTTP/2, Cloudflare Access via labels

**Independent Test**: Configure container with originRequest labels, verify Cloudflare config includes all specified settings

### Unit Tests for User Story 6

- [X] T076 [P] [US6] Write unit test for OriginRequestConfig parsing in internal/controller/label_parser_test.go (all 20+ originRequest attributes)
- [ ] T077 [P] [US6] Write unit test for global defaults merging with container labels in internal/controller/label_parser_test.go
- [X] T078 [P] [US6] Write unit test for Cloudflare Access config parsing in internal/controller/label_parser_test.go

### Implementation for User Story 6

- [X] T079 [P] [US6] Extend OriginRequestConfig type with all 20+ attributes in pkg/types/tunnel.go (noTLSVerify, connectTimeout, tlsTimeout, tcpKeepAlive, keepAliveConnections, keepAliveTimeout, noHappyEyeballs, proxyType, proxyAddress, proxyPort, httpHostHeader, originServerName, matchSniToHost, caPool, http2Origin, disableChunkedEncoding)
- [X] T080 [US6] Add AccessConfig sub-structure to OriginRequestConfig in pkg/types/tunnel.go (required, teamName, audTag)
- [X] T081 [US6] Implement comprehensive originRequest attribute parser in internal/controller/label_parser.go (parse all docktunnel.<name>.originRequest.* labels)
- [ ] T082 [US6] Implement global defaults for OriginRequestConfig in internal/config/config.go (populate from cloudflare section of config.yaml)
- [ ] T083 [US6] Implement merge strategy for originRequest settings in internal/controller/label_parser.go (start with global defaults, override with container labels)
- [ ] T084 [US6] Implement duration to nanoseconds conversion for Cloudflare API in internal/cloudflareManager/tunnel.go (Cloudflare expects nanoseconds for timeouts)
- [ ] T085 [US6] Add comprehensive logging for originRequest configuration in internal/controller/label_parser.go (log which settings applied from defaults vs labels)

**Checkpoint**: At this point, ALL user stories should be fully functional - complete enterprise-grade configuration control

---

## Phase 9: Integration & Edge Case Handling

**Purpose**: Integration tests, flapping detection, and production hardening

### Integration Tests

- [ ] T086 [P] Create integration test for Docker event handling in tests/integration/docker_events_test.go (use real Docker daemon, test container start/stop)
- [ ] T087 [P] Create end-to-end integration test in tests/integration/end_to_end_test.go (full lifecycle: container start → tunnel created → container stop → tunnel removed)
- [ ] T088 [P] Create integration test for concurrent container starts in tests/integration/docker_events_test.go (100 containers simultaneously)

### Flapping Detection

- [ ] T089 [P] Define FlappingState and FlappingDetector types in pkg/types/tunnel.go (transitions array, cooling period)
- [ ] T090 Implement flapping detection algorithm in internal/state/manager.go (sliding window counter, 60s observation, 300s cooling)
- [ ] T091 Add flapping detection to controller event handler in internal/controller/controller.go (track state transitions, mark as FLAPPING)
- [ ] T092 Add logging for flapping detection events in internal/controller/controller.go (WARN when flapping detected, INFO when cooling period expires)

### Additional Edge Cases

- [ ] T093 [P] Add validation for containers without exposed ports in internal/controller/validator.go (log warning, skip container)
- [ ] T094 [P] Add validation for malformed service URLs in internal/controller/validator.go (reject with clear error message)
- [ ] T095 [P] Add validation for duplicate hostnames across containers in internal/controller/validator.go (reject new config, log conflicting container IDs)

---

## Phase 10: Polish & Cross-Cutting Concerns

**Purpose**: Documentation, performance validation, security hardening

### Documentation

- [ ] T096 [P] Update README.md with complete usage examples (label examples, configuration guide, deployment instructions)
- [ ] T097 [P] Update CLAUDE.md with implementation details (module descriptions, data flow diagrams)
- [ ] T098 [P] Create example docker-compose.yml for development and production deployment in repository root

### Performance & Resource Management

- [ ] T099 Run performance test with 100 containers to validate < 100MB memory footprint (use go tool pprof)
- [ ] T100 Validate 5-second event processing SLA with timing benchmarks in controller event handler
- [ ] T101 Add memory profiling support to cmd/docktunnel/main.go (optional --cpuprofile and --memprofile flags)

### Security Hardening

- [ ] T102 Add label input validation to prevent injection attacks in internal/controller/label_parser.go (sanitize label values, reject shell metacharacters)
- [ ] T103 Add API token validation on startup in internal/config/config.go (test Cloudflare API call, fail fast if invalid)
- [ ] T104 Ensure API tokens and secrets never appear in logs in internal/logger/logger.go (sanitize log output)

### Final Validation

- [ ] T105 Run full test suite (make test) and ensure 100% pass rate
- [ ] T106 Validate quickstart.md instructions work end-to-end (follow guide, verify all commands succeed)
- [ ] T107 Run constitution compliance check (verify all 6 principles satisfied)

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies - can start immediately
- **Foundational (Phase 2)**: Depends on Setup completion - BLOCKS all user stories
- **User Stories (Phase 3-8)**: All depend on Foundational phase completion
  - User Story 1 (P1): Can start after Foundational - MVP deliverable
  - User Story 4 (P1): Can start after Foundational - enhances US1 with auto-detection
  - User Story 2 (P2): Can start after Foundational - independent of US1/US4 but same codebase
  - User Story 3 (P2): Depends on US1 controller event handling - extends stop logic
  - User Story 5 (P3): Depends on US3 retention policies - adds persistence
  - User Story 6 (P3): Can start after Foundational - extends label parser
- **Integration (Phase 9)**: Depends on US1, US2, US3, US4 completion
- **Polish (Phase 10)**: Depends on all desired user stories being complete

### User Story Dependencies

- **User Story 1 (P1 - MVP)**: No dependencies on other stories - core event handling and Cloudflare sync
- **User Story 4 (P1 - Auto-detection)**: No dependencies on other stories - extends label parser from US1
- **User Story 2 (P2 - Traefik)**: No dependencies on other stories - adds alternative label parsing path
- **User Story 3 (P2 - Retention)**: Depends on US1 (needs event handling) - extends container stop logic
- **User Story 5 (P3 - State Persistence)**: Depends on US3 (needs retention policy state) - adds persistence layer
- **User Story 6 (P3 - Origin Request)**: No dependencies on other stories - extends configuration parsing

### Recommended Implementation Order

1. **Setup (Phase 1)**: Complete foundational infrastructure
2. **Foundational (Phase 2)**: Build core components all stories depend on
3. **MVP Sprint (Phase 3)**: Deliver User Story 1 + User Story 4 together (complete basic functionality)
4. **Traefik Support Sprint (Phase 5)**: Add User Story 2 (migration path)
5. **Retention Sprint (Phase 6)**: Add User Story 3 (production stability)
6. **Persistence Sprint (Phase 7)**: Add User Story 5 (fault tolerance)
7. **Enterprise Sprint (Phase 8)**: Add User Story 6 (advanced configuration)
8. **Production Hardening (Phase 9)**: Integration tests, flapping detection, edge cases
9. **Release Prep (Phase 10)**: Documentation, performance validation, security

### Parallel Opportunities

**Within Setup (Phase 1)**:
- T003, T004, T005, T006 can all run in parallel (different files)

**Within Foundational (Phase 2)**:
- T009, T010 can run in parallel (different type definitions)
- T012, T013 can run in parallel (different test files)

**Within User Story 1 (Phase 3)**:
- T014, T015, T016 (mock creation) can run in parallel
- T017-T024 (individual component implementations) can run in parallel once mocks exist
- Only T025-T030 have dependencies on earlier T017-T024 tasks

**Across User Stories**:
- Once Foundational phase completes, US1, US4, US2, US6 can all proceed in parallel (different developers or sequential by priority)
- US3 depends on US1
- US5 depends on US3

**Integration & Polish (Phase 9-10)**:
- T086-T088 can run in parallel
- T089-T095 can run in parallel
- T096-T098 can run in parallel
- T099-T0107 can run in parallel

---

## Parallel Example: User Story 1 MVP Sprint

```bash
# Terminal 1: Mock infrastructure (can run in parallel)
go test -run TestMockCloudflareClient ./tests/mocks/
go test -run TestMockDockerClient ./tests/mocks/

# Terminal 2: Docker integration (can run in parallel with mocks)
go test -run TestDockerEventStream ./internal/docker/

# Terminal 3: Label parsing and validation (can run in parallel)
go test -run TestLabelParser ./internal/controller/
go test -run TestValidator ./internal/controller/

# Terminal 4: Cloudflare manager (can run in parallel)
go test -run TestCloudflareClient ./internal/cloudflareManager/
go test -run TestRateLimiter ./internal/cloudflareManager/

# Terminal 5: State management (can run in parallel)
go test -run TestStateManager ./internal/state/

# After all components pass individually, run integration test
go test -run TestEndToEnd ./tests/integration/
```

---

## MVP Scope Recommendation

**Minimum Viable Product (MVP)**: Deliver **Phase 1 (Setup) + Phase 2 (Foundational) + Phase 3 (User Story 1) + Phase 4 (User Story 4)**

This delivers:
- ✅ Automatic container exposure via Cloudflare Tunnel
- ✅ 4-layer configuration priority (DockTunnel → Traefik → Auto-detect → Defaults)
- ✅ Basic label parsing with hostname and service URL
- ✅ Event-driven architecture (container start → tunnel creation within 5 seconds)
- ✅ Graceful shutdown
- ✅ Structured logging
- ✅ Hostname uniqueness validation

**MVP Excludes** (can be added in subsequent sprints):
- Traefik compatibility (User Story 2)
- Retention policies (User Story 3)
- State persistence (User Story 5)
- Advanced origin request config (User Story 6)
- Flapping detection
- Integration tests

---

## Task Count Summary

- **Total Tasks**: 107
- **Setup (Phase 1)**: 6 tasks
- **Foundational (Phase 2)**: 7 tasks
- **User Story 1 (Phase 3)**: 17 tasks (MVP core)
- **User Story 4 (Phase 4)**: 14 tasks (MVP auto-detection)
- **User Story 2 (Phase 5)**: 10 tasks (Traefik compatibility)
- **User Story 3 (Phase 6)**: 11 tasks (Retention policies)
- **User Story 5 (Phase 7)**: 10 tasks (State persistence)
- **User Story 6 (Phase 8)**: 10 tasks (Advanced origin config)
- **Integration (Phase 9)**: 10 tasks (Edge cases, flapping, integration tests)
- **Polish (Phase 10)**: 12 tasks (Documentation, performance, security)

**Parallelizable Tasks**: 67 tasks marked with [P] (63% of total)

**Test Coverage**:
- Unit test tasks: 24
- Integration test tasks: 3
- Total test tasks: 27 (25% of all tasks)

---

## Implementation Strategy

1. **Start with Setup (Phase 1)**: 1-2 hours - project structure and dependencies
2. **Build Foundation (Phase 2)**: 4-6 hours - logging, config, types
3. **MVP Sprint (Phase 3+4)**: 16-24 hours - core automatic tunnel functionality
   - Deliver working MVP that exposes labeled containers
   - Demo to stakeholders for feedback
4. **Traefik Sprint (Phase 5)**: 8-12 hours - migration path for existing users
5. **Production Sprint (Phase 6)**: 8-12 hours - retention policies for stability
6. **Fault Tolerance Sprint (Phase 7)**: 6-10 hours - state persistence for recovery
7. **Enterprise Sprint (Phase 8)**: 8-12 hours - advanced configuration options
8. **Hardening Sprint (Phase 9+10)**: 8-12 hours - tests, edge cases, polish

**Total Estimated Effort**: 60-90 hours (excluding testing overhead and debugging)

**Recommended Team Size**: 1-2 developers
- Solo developer: ~3 weeks (sequential implementation by priority)
- Two developers: ~2 weeks (parallel work on independent user stories after foundational phase)
