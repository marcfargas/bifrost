"""Tests for the TaskHub."""

import pytest

from bifrost.hub.tasks import InvalidTransition, TaskHub
from bifrost.store.db import Store
from bifrost.store.models import Artifact, TaskStatus, TextPart


@pytest.fixture
def hub(tmp_path):
    store = Store(tmp_path / "test.db")
    yield TaskHub(store)
    store.close()


class TestCreate:
    def test_basic(self, hub):
        task = hub.create(requester="a1", assignee="a2")
        assert task.requester == "a1"
        assert task.assignee == "a2"
        assert task.status == TaskStatus.QUEUED
        assert len(task.id) == 12

    def test_with_context(self, hub):
        task = hub.create(requester="a1", assignee="a2", context_id="conv-1")
        assert task.context_id == "conv-1"

    def test_with_metadata(self, hub):
        task = hub.create(requester="a1", assignee="a2", metadata={"priority": "high"})
        assert task.metadata == {"priority": "high"}


class TestGet:
    def test_exists(self, hub):
        t = hub.create(requester="a1", assignee="a2")
        got = hub.get(t.id)
        assert got.id == t.id

    def test_missing(self, hub):
        with pytest.raises(KeyError):
            hub.get("nope")


class TestUpdateStatus:
    def test_queued_to_running(self, hub):
        t = hub.create(requester="a1", assignee="a2")
        updated = hub.update_status(t.id, TaskStatus.RUNNING)
        assert updated.status == TaskStatus.RUNNING

    def test_queued_to_rejected(self, hub):
        t = hub.create(requester="a1", assignee="a2")
        updated = hub.update_status(t.id, TaskStatus.REJECTED)
        assert updated.status == TaskStatus.REJECTED

    def test_queued_to_canceled(self, hub):
        t = hub.create(requester="a1", assignee="a2")
        updated = hub.update_status(t.id, TaskStatus.CANCELED)
        assert updated.status == TaskStatus.CANCELED

    def test_running_to_completed(self, hub):
        t = hub.create(requester="a1", assignee="a2")
        hub.update_status(t.id, TaskStatus.RUNNING)
        updated = hub.update_status(t.id, TaskStatus.COMPLETED)
        assert updated.status == TaskStatus.COMPLETED

    def test_running_to_failed(self, hub):
        t = hub.create(requester="a1", assignee="a2")
        hub.update_status(t.id, TaskStatus.RUNNING)
        updated = hub.update_status(t.id, TaskStatus.FAILED)
        assert updated.status == TaskStatus.FAILED

    def test_running_to_input_required(self, hub):
        t = hub.create(requester="a1", assignee="a2")
        hub.update_status(t.id, TaskStatus.RUNNING)
        updated = hub.update_status(t.id, TaskStatus.INPUT_REQUIRED)
        assert updated.status == TaskStatus.INPUT_REQUIRED

    def test_running_to_auth_required(self, hub):
        t = hub.create(requester="a1", assignee="a2")
        hub.update_status(t.id, TaskStatus.RUNNING)
        updated = hub.update_status(t.id, TaskStatus.AUTH_REQUIRED)
        assert updated.status == TaskStatus.AUTH_REQUIRED

    def test_input_required_to_running(self, hub):
        t = hub.create(requester="a1", assignee="a2")
        hub.update_status(t.id, TaskStatus.RUNNING)
        hub.update_status(t.id, TaskStatus.INPUT_REQUIRED)
        updated = hub.update_status(t.id, TaskStatus.RUNNING)
        assert updated.status == TaskStatus.RUNNING

    def test_auth_required_to_canceled(self, hub):
        t = hub.create(requester="a1", assignee="a2")
        hub.update_status(t.id, TaskStatus.RUNNING)
        hub.update_status(t.id, TaskStatus.AUTH_REQUIRED)
        updated = hub.update_status(t.id, TaskStatus.CANCELED)
        assert updated.status == TaskStatus.CANCELED

    # Invalid transitions
    def test_queued_to_completed_invalid(self, hub):
        t = hub.create(requester="a1", assignee="a2")
        with pytest.raises(InvalidTransition):
            hub.update_status(t.id, TaskStatus.COMPLETED)

    def test_completed_to_running_invalid(self, hub):
        t = hub.create(requester="a1", assignee="a2")
        hub.update_status(t.id, TaskStatus.RUNNING)
        hub.update_status(t.id, TaskStatus.COMPLETED)
        with pytest.raises(InvalidTransition):
            hub.update_status(t.id, TaskStatus.RUNNING)

    def test_failed_is_terminal(self, hub):
        t = hub.create(requester="a1", assignee="a2")
        hub.update_status(t.id, TaskStatus.RUNNING)
        hub.update_status(t.id, TaskStatus.FAILED)
        with pytest.raises(InvalidTransition):
            hub.update_status(t.id, TaskStatus.RUNNING)

    def test_rejected_is_terminal(self, hub):
        t = hub.create(requester="a1", assignee="a2")
        hub.update_status(t.id, TaskStatus.REJECTED)
        with pytest.raises(InvalidTransition):
            hub.update_status(t.id, TaskStatus.QUEUED)

    def test_canceled_is_terminal(self, hub):
        t = hub.create(requester="a1", assignee="a2")
        hub.update_status(t.id, TaskStatus.CANCELED)
        with pytest.raises(InvalidTransition):
            hub.update_status(t.id, TaskStatus.RUNNING)

    def test_persists_to_store(self, hub):
        t = hub.create(requester="a1", assignee="a2")
        hub.update_status(t.id, TaskStatus.RUNNING)
        got = hub.get(t.id)
        assert got.status == TaskStatus.RUNNING


class TestUpdateArtifacts:
    def test_basic(self, hub):
        t = hub.create(requester="a1", assignee="a2")
        art = Artifact(id="art1", parts=[TextPart("result")])
        updated = hub.update_artifacts(t.id, [art])
        assert len(updated.artifacts) == 1

    def test_persists(self, hub):
        t = hub.create(requester="a1", assignee="a2")
        art = Artifact(id="art1", parts=[TextPart("result")])
        hub.update_artifacts(t.id, [art])
        got = hub.get(t.id)
        assert len(got.artifacts) == 1
        assert got.artifacts[0].parts[0].text == "result"


class TestList:
    def test_all(self, hub):
        hub.create(requester="a1", assignee="a2")
        hub.create(requester="a1", assignee="a3")
        assert len(hub.list()) == 2

    def test_by_status(self, hub):
        t = hub.create(requester="a1", assignee="a2")
        hub.create(requester="a1", assignee="a3")
        hub.update_status(t.id, TaskStatus.RUNNING)
        assert len(hub.list(status=TaskStatus.RUNNING)) == 1
        assert len(hub.list(status=TaskStatus.QUEUED)) == 1

    def test_by_requester(self, hub):
        hub.create(requester="a1", assignee="a2")
        hub.create(requester="a2", assignee="a3")
        assert len(hub.list(requester="a1")) == 1

    def test_by_assignee(self, hub):
        hub.create(requester="a1", assignee="a2")
        hub.create(requester="a1", assignee="a3")
        assert len(hub.list(assignee="a2")) == 1
