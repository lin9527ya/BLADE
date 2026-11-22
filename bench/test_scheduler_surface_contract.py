#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""请求结构归因的跨语言契约测试。

Go 的 db.RequestSurfaceSummary 与 bench/scheduler.py 读取的字段名必须一致。
0.9.25 之前 Go 结构体没有 json tag，输出 Worker/SystemSHA，而这里读 worker/system_sha，
真实归因被静默读成空串/0。本测试同时锁定「接受正确契约」和「拒绝错误契约」。
"""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import scheduler  # noqa: E402


def surface_row(**overrides):
    row = {
        "worker": "planner",
        "model": "deepseek-v4.1-flash",
        "format": "chat",
        "build_version": "0.9.25",
        "system_sha": "sys",
        "tools_sha": "tools",
        "parameters_sha": "params",
        "endpoint_sha": "endpoint",
        "calls": 3,
        "changed_calls": 0,
        "avg_input": 1200,
        "avg_output": 300,
        "cache_hit_tokens": 400,
    }
    row.update(overrides)
    return row


class SurfaceContractTest(unittest.TestCase):
    def client_with(self, fixture, status=200):
        client = scheduler.ArtexClient()
        client._call = lambda method, path, body=None: (status, fixture)
        return client

    def test_accepts_canonical_payload(self):
        client = self.client_with({"surfaces": [surface_row()]})
        rows = client.request_surfaces(7)
        self.assertEqual(rows[0]["worker"], "planner")
        self.assertEqual(rows[0]["system_sha"], "sys")
        self.assertEqual(rows[0]["calls"], 3)

    def test_accepts_empty_surfaces(self):
        self.assertEqual(self.client_with({"surfaces": []}).request_surfaces(7), [])

    def test_rejects_missing_surfaces_key(self):
        with self.assertRaises(scheduler.SurfaceContractError):
            self.client_with({}).request_surfaces(7)

    def test_rejects_non_2xx(self):
        with self.assertRaises(scheduler.SurfaceContractError):
            self.client_with({"error": "boom"}, status=503).request_surfaces(7)

    def test_rejects_go_default_field_names(self):
        # 这是 0.9.25 之前的真实故障形态：字段名漂移后旧实现全部读成默认值。
        pascal = {k.title().replace("_", ""): v for k, v in surface_row().items()}
        with self.assertRaises(scheduler.SurfaceContractError) as ctx:
            self.client_with({"surfaces": [pascal]}).request_surfaces(7)
        self.assertIn("缺少字段", str(ctx.exception))

    def test_rejects_missing_field(self):
        row = surface_row()
        row.pop("model")
        with self.assertRaises(scheduler.SurfaceContractError):
            self.client_with({"surfaces": [row]}).request_surfaces(7)

    def test_rejects_wrong_numeric_type(self):
        with self.assertRaises(scheduler.SurfaceContractError):
            self.client_with({"surfaces": [surface_row(calls="3")]}).request_surfaces(7)

    def test_rejects_bool_as_int(self):
        with self.assertRaises(scheduler.SurfaceContractError):
            self.client_with({"surfaces": [surface_row(calls=True)]}).request_surfaces(7)

    def test_rejects_empty_identity(self):
        for field in ("model", "system_sha", "tools_sha", "parameters_sha", "endpoint_sha"):
            with self.subTest(field=field):
                with self.assertRaises(scheduler.SurfaceContractError):
                    self.client_with({"surfaces": [surface_row(**{field: "  "})]}).request_surfaces(7)

    def test_rejects_zero_calls(self):
        with self.assertRaises(scheduler.SurfaceContractError):
            self.client_with({"surfaces": [surface_row(calls=0)]}).request_surfaces(7)

    def test_rejects_changed_calls_above_calls(self):
        with self.assertRaises(scheduler.SurfaceContractError):
            self.client_with({"surfaces": [surface_row(calls=2, changed_calls=3)]}).request_surfaces(7)


class MetricsSurfaceCollectionTest(unittest.TestCase):
    def metrics_with(self, rows, client):
        metrics = scheduler.RunMetrics.__new__(scheduler.RunMetrics)
        metrics.simulate = False
        metrics.rows = rows

        class FakeSched:
            a = client

        return metrics, FakeSched()

    def test_collect_keeps_attribution_error(self):
        class Client:
            def request_surfaces(self, task_id):
                if str(task_id) == "11":
                    return [surface_row()]
                raise scheduler.SurfaceContractError("surfaces[0] 缺少字段: model")

        metrics, sched = self.metrics_with([{"task_id": "11"}, {"task_id": "12"}], Client())
        surfaces, err = metrics._collect_surfaces(sched)
        self.assertIn("11", surfaces)
        self.assertIn("12", err)
        self.assertIn("model", err)

    def test_collect_is_empty_in_simulate_mode(self):
        metrics, sched = self.metrics_with([{"task_id": "11"}], None)
        metrics.simulate = True
        self.assertEqual(metrics._collect_surfaces(sched), ({}, ""))


if __name__ == "__main__":
    unittest.main()
