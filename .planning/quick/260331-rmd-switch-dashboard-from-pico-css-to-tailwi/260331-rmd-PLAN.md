---
phase: quick
plan: 1
type: execute
wave: 1
depends_on: []
files_modified:
  - src/bifrost/dashboard/templates/base.html
  - src/bifrost/dashboard/templates/agents.html
  - src/bifrost/dashboard/templates/tasks.html
  - src/bifrost/dashboard/templates/conversations.html
  - src/bifrost/dashboard/templates/activity.html
  - src/bifrost/dashboard/templates/partials/agent_list.html
  - src/bifrost/dashboard/templates/partials/task_list.html
  - src/bifrost/dashboard/templates/partials/conversation_list.html
  - src/bifrost/dashboard/templates/partials/conversation_detail.html
  - src/bifrost/dashboard/templates/partials/activity_feed.html
autonomous: true
requirements: []
must_haves:
  truths:
    - "Dashboard loads with Tailwind CDN instead of Pico CSS"
    - "Sidebar nav renders as a dark fixed sidebar with active page highlighting"
    - "Agent cards display in a responsive grid layout"
    - "Task board has styled filter buttons and a proper data table"
    - "Conversations show two-panel layout (list + detail)"
    - "Activity feed displays as a timeline-style list"
    - "All htmx polling and swap attributes remain intact"
    - "All existing tests pass unchanged"
  artifacts:
    - path: "src/bifrost/dashboard/templates/base.html"
      provides: "Tailwind CDN base layout with dark sidebar nav"
      contains: "cdn.tailwindcss.com"
    - path: "src/bifrost/dashboard/templates/partials/agent_list.html"
      provides: "Agent card grid with Tailwind classes"
      contains: "human-card"
  key_links:
    - from: "src/bifrost/dashboard/templates/agents.html"
      to: "/agents/list"
      via: "hx-get on #agent-list div"
      pattern: 'hx-get="/agents/list"'
    - from: "src/bifrost/dashboard/templates/tasks.html"
      to: "/tasks/list"
      via: "hx-get filter buttons and #task-list div"
      pattern: 'hx-get="/tasks/list'
    - from: "src/bifrost/dashboard/templates/conversations.html"
      to: "/conversations/list"
      via: "hx-get on #conv-list div"
      pattern: 'hx-get="/conversations/list"'
    - from: "src/bifrost/dashboard/templates/partials/conversation_list.html"
      to: "/conversations/{id}/messages"
      via: "hx-get on each conv-item"
      pattern: 'hx-get="/conversations/{{ conv.id }}/messages"'
    - from: "src/bifrost/dashboard/templates/activity.html"
      to: "/activity/feed"
      via: "hx-get on #activity-feed div"
      pattern: 'hx-get="/activity/feed"'
---

<objective>
Replace Pico CSS classless with Tailwind CDN across all dashboard templates. Transform the current unstyled layout into a proper dashboard with dark sidebar navigation, card grids, styled tables, two-panel conversation view, and timeline activity feed.

Purpose: The Pico CSS classless approach produces a broken/unstyled layout. Tailwind CDN gives full control over dashboard styling without a build toolchain.
Output: All 10 template files rewritten with Tailwind utility classes, visually cohesive dashboard.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@src/bifrost/dashboard/routes.py
@tests/test_dashboard.py
</context>

<tasks>

<task type="auto">
  <name>Task 1: Replace base layout and all page templates with Tailwind</name>
  <files>
    src/bifrost/dashboard/templates/base.html,
    src/bifrost/dashboard/templates/agents.html,
    src/bifrost/dashboard/templates/tasks.html,
    src/bifrost/dashboard/templates/conversations.html,
    src/bifrost/dashboard/templates/activity.html
  </files>
  <action>
    Rewrite base.html:
    - Remove Pico CSS CDN link. Add Tailwind CDN: `<script src="https://cdn.tailwindcss.com"></script>` (play CDN, no build needed).
    - Keep htmx script tag exactly as-is (same src, integrity, crossorigin).
    - Build a sidebar+main layout using Tailwind:
      - Sidebar: fixed left, w-56, full height, dark background (bg-gray-900 text-white), with "Bifrost" heading and nav links.
      - Nav links: styled as block items with hover bg (hover:bg-gray-700), rounded. Active page uses `aria-current="page"` (keep existing Jinja2 conditional) AND a visible bg (bg-gray-700).
      - Main content: ml-56 (offset for sidebar), p-6, bg-gray-50 min-h-screen.
    - Remove ALL custom CSS from `<style>` block. Everything moves to Tailwind utility classes.
    - Keep Jinja2 blocks: `{% block title %}` and `{% block content %}`.

    Rewrite agents.html:
    - Keep extends, block title, block content structure.
    - "Agent Directory" as text-2xl font-bold mb-4.
    - Keep #agent-list div with all hx-* attributes exactly as-is.
    - Add "Loading..." with text-gray-500 styling.

    Rewrite tasks.html:
    - Keep extends, block title, block content.
    - "Task Board" as text-2xl font-bold mb-4.
    - Style filter buttons: flex gap-2 mb-4, each button gets Tailwind classes (px-3 py-1 rounded bg-gray-200 hover:bg-gray-300 text-sm). Keep ALL hx-get, hx-target, hx-swap attributes exactly as they are.
    - Keep #task-list div with all hx-* attributes exactly as-is.

    Rewrite conversations.html:
    - Keep extends, block title, block content.
    - "Conversations" as text-2xl font-bold mb-4.
    - Two-panel grid: use Tailwind grid grid-cols-3 gap-4. Left panel (col-span-1) = #conv-list. Right panel (col-span-2) = #conv-detail.
    - Keep ALL hx-* attributes on #conv-list exactly as-is.
    - Keep #conv-detail with its placeholder text.

    Rewrite activity.html:
    - Keep extends, block title, block content.
    - "Activity Feed" as text-2xl font-bold mb-4.
    - Keep #activity-feed div with all hx-* attributes exactly as-is.

    CRITICAL: Do NOT change any hx-get, hx-target, hx-swap, hx-trigger attributes. Do NOT change any Jinja2 template variable references or block names. Do NOT change element IDs (agent-list, task-list, conv-list, conv-detail, activity-feed).
  </action>
  <verify>
    <automated>cd /home/marc/dev/bifrost && python -m pytest tests/test_dashboard.py -x -q</automated>
  </verify>
  <done>
    base.html uses Tailwind CDN instead of Pico CSS. Sidebar nav is styled with dark background. All 4 page templates use Tailwind utility classes. All htmx attributes preserved. All existing tests pass.
  </done>
</task>

<task type="auto">
  <name>Task 2: Restyle all partial templates with Tailwind</name>
  <files>
    src/bifrost/dashboard/templates/partials/agent_list.html,
    src/bifrost/dashboard/templates/partials/task_list.html,
    src/bifrost/dashboard/templates/partials/conversation_list.html,
    src/bifrost/dashboard/templates/partials/conversation_detail.html,
    src/bifrost/dashboard/templates/partials/activity_feed.html
  </files>
  <action>
    Rewrite partials/agent_list.html:
    - Human Operator section: Keep the `human-card` class (test depends on it). Add Tailwind: border-l-4 border-blue-500 pl-4 p-4 bg-white rounded-lg shadow-sm mb-6.
    - Keep `status-dot` class (test depends on it). Add Tailwind alongside: w-2 h-2 rounded-full inline-block mr-1. Add color via additional Tailwind classes per status (online=bg-blue-500, idle=bg-yellow-500, offline=bg-gray-400, dnd=bg-red-600).
    - "Human" badge: inline-flex px-2 py-0.5 text-xs font-medium rounded bg-purple-100 text-purple-700.
    - Agent card grid: grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-4.
    - Each agent card: bg-white rounded-lg shadow-sm p-4, with status dot, name bold, last seen as text-gray-500 text-sm.
    - Keep ALL Jinja2 logic (if/for/endif) exactly as-is. Keep all template variable references.

    Rewrite partials/task_list.html:
    - Table: w-full, thead with bg-gray-100 text-left text-sm font-medium text-gray-600.
    - Table cells: px-4 py-3, border-b border-gray-100.
    - Status badges: Keep badge and badge-{status} classes. Add Tailwind alongside: inline-flex px-2 py-0.5 text-xs font-semibold rounded-full. Colors: queued=bg-blue-100 text-blue-700, running=bg-yellow-100 text-yellow-700, completed=bg-green-100 text-green-700, failed=bg-red-100 text-red-700, canceled/rejected=bg-gray-100 text-gray-500.
    - Keep ALL Jinja2 loops and variable references.

    Rewrite partials/conversation_list.html:
    - Each conversation item: bg-white rounded-lg p-3 mb-2 shadow-sm hover:bg-gray-50 cursor-pointer transition.
    - Keep ALL hx-get, hx-target, hx-swap attributes on each conv item.
    - Title bold, channel as text-gray-500 text-sm, timestamp as text-gray-400 text-xs.

    Rewrite partials/conversation_detail.html:
    - Conversation title: text-xl font-bold. Channel: text-gray-500 text-sm.
    - Separator: border-t border-gray-200 my-3 (replace <hr>).
    - Each message: bg-white rounded-lg p-4 mb-3 shadow-sm. Header: flex justify-between, sender bold, timestamp text-gray-400 text-sm.
    - Message text: mt-2. File/data indicators styled with bg-gray-100 p-2 rounded text-sm.
    - Keep ALL Jinja2 logic for event type handling (message vs participant.added etc).

    Rewrite partials/activity_feed.html:
    - Each event: bg-white rounded-lg p-4 mb-3 shadow-sm.
    - Header: flex items-center gap-2, timestamp text-gray-400 text-xs, event type as inline badge (px-2 py-0.5 text-xs rounded bg-gray-200), agent name font-semibold.
    - Event data: mt-1 text-gray-700.

    CRITICAL: Preserve these CSS class names (tests depend on them): `human-card`, `status-dot`. Keep all badge/badge-{status} classes as well (no test dependency but good to keep for consistency). Do NOT modify any Jinja2 template logic, variable names, or htmx attributes.
  </action>
  <verify>
    <automated>cd /home/marc/dev/bifrost && python -m pytest tests/test_dashboard.py -x -q</automated>
  </verify>
  <done>
    All 6 partial templates use Tailwind utility classes. Agent cards display in responsive grid. Task table is properly styled. Conversation list items are cards with hover states. Message detail has clear sender/timestamp layout. Activity feed shows timeline-style events. All existing tests pass.
  </done>
</task>

<task type="checkpoint:human-verify" gate="blocking">
  <what-built>Complete Tailwind dashboard replacing Pico CSS: dark sidebar nav, agent card grid, styled task table with filter buttons, two-panel conversation view, timeline activity feed.</what-built>
  <how-to-verify>
    1. Start the server: `python -m bifrost --insecure`
    2. Visit http://localhost:8000/agents -- verify dark sidebar with nav links, agent cards in grid layout, Human Operator card with blue left border
    3. Visit http://localhost:8000/tasks -- verify filter buttons styled as pills, table with proper headers and striped rows
    4. Visit http://localhost:8000/conversations -- verify two-panel layout (list left, detail right)
    5. Visit http://localhost:8000/activity -- verify timeline-style event cards
    6. Check responsive: resize browser to mobile width, verify sidebar and content adapt
    7. Verify htmx still works: filter buttons on tasks page swap content, conversation list items load detail panel
  </how-to-verify>
  <resume-signal>Type "approved" or describe issues to fix</resume-signal>
</task>

</tasks>

<verification>
- All 10 template files rewritten with Tailwind utility classes
- No Pico CSS references remain in any template
- htmx CDN script tag unchanged (same src, integrity hash)
- All hx-* attributes on all elements unchanged
- All Jinja2 blocks, variables, filters, and logic unchanged
- All element IDs unchanged (agent-list, task-list, conv-list, conv-detail, activity-feed)
- `python -m pytest tests/test_dashboard.py -x -q` passes with no failures
</verification>

<success_criteria>
- Dashboard renders with Tailwind styling: dark sidebar, card grids, styled tables
- All 10 templates use Tailwind CDN (no Pico CSS)
- All existing tests in test_dashboard.py pass without modification
- htmx polling and partial swaps continue to work
</success_criteria>

<output>
After completion, create `.planning/quick/260331-rmd-switch-dashboard-from-pico-css-to-tailwi/260331-rmd-SUMMARY.md`
</output>
