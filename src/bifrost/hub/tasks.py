"""Hub layer for task lifecycle management with A2A transition validation."""

from __future__ import annotations

from typing import Any

from bifrost.store.db import Store
from bifrost.store.models import (
    Artifact,
    Task,
    TaskStatus,
    _new_id,
    _now,
)


class InvalidTransition(Exception):
    """Raised when a task status transition is not allowed."""


# A2A valid transitions
_TRANSITIONS: dict[TaskStatus, set[TaskStatus]] = {
    TaskStatus.QUEUED: {TaskStatus.RUNNING, TaskStatus.REJECTED, TaskStatus.CANCELED},
    TaskStatus.RUNNING: {
        TaskStatus.COMPLETED, TaskStatus.FAILED, TaskStatus.CANCELED,
        TaskStatus.INPUT_REQUIRED, TaskStatus.AUTH_REQUIRED,
    },
    TaskStatus.INPUT_REQUIRED: {TaskStatus.RUNNING, TaskStatus.CANCELED, TaskStatus.FAILED},
    TaskStatus.AUTH_REQUIRED: {TaskStatus.RUNNING, TaskStatus.CANCELED, TaskStatus.FAILED},
    # Terminal states
    TaskStatus.COMPLETED: set(),
    TaskStatus.FAILED: set(),
    TaskStatus.CANCELED: set(),
    TaskStatus.REJECTED: set(),
}


class TaskHub:
    """Manages task creation, status transitions, and artifacts."""

    def __init__(self, store: Store) -> None:
        self._store = store

    def create(
        self,
        requester: str,
        assignee: str,
        context_id: str | None = None,
        metadata: dict[str, Any] | None = None,
    ) -> Task:
        """Create a new task in QUEUED status."""
        task = Task(
            id=_new_id(),
            requester=requester,
            assignee=assignee,
            context_id=context_id,
            metadata=metadata or {},
        )
        self._store.create_task(task)
        return task

    def get(self, task_id: str) -> Task:
        """Get a task by ID."""
        task = self._store.get_task(task_id)
        if task is None:
            raise KeyError(f"Task not found: {task_id}")
        return task

    def update_status(self, task_id: str, status: TaskStatus) -> Task:
        """Update task status with A2A transition validation."""
        task = self.get(task_id)
        current = task.status
        allowed = _TRANSITIONS.get(current, set())
        if status not in allowed:
            raise InvalidTransition(
                f"Cannot transition from {current!r} to {status!r}"
            )
        now = _now()
        self._store.update_task(task_id, status=status, updated_at=now)
        task.status = status
        task.updated_at = now
        return task

    def update_artifacts(self, task_id: str, artifacts: list[Artifact]) -> Task:
        """Replace the task's artifacts list."""
        task = self.get(task_id)
        now = _now()
        self._store.update_task(task_id, artifacts=artifacts, updated_at=now)
        task.artifacts = artifacts
        task.updated_at = now
        return task

    def list(
        self,
        status: TaskStatus | None = None,
        requester: str | None = None,
        assignee: str | None = None,
    ) -> list[Task]:
        """List tasks with optional filters."""
        return self._store.list_tasks(status=status, requester=requester, assignee=assignee)
