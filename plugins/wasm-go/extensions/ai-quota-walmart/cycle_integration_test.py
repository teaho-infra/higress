#!/usr/bin/env python3
"""Exercise money budget cycles against disposable Redis on port 16389."""
import datetime as dt
import json
import pathlib
import socket
import uuid

s = socket.create_connection(("127.0.0.1", 16389))
f = s.makefile("rb")

def read():
    kind = f.read(1)
    line = f.readline().strip()
    if kind == b"$":
        count = int(line)
        if count < 0:
            return None
        value = f.read(count)
        f.read(2)
        return value.decode()
    if kind == b"*":
        return [read() for _ in range(int(line))]
    if kind == b"-":
        raise RuntimeError(line.decode())
    return line.decode()

def command(*args):
    data = [str(arg).encode() for arg in args]
    s.sendall(b"*" + str(len(data)).encode() + b"\r\n" + b"".join(
        b"$" + str(len(item)).encode() + b"\r\n" + item + b"\r\n" for item in data
    ))
    return read()

prefix = "cycle-test:" + str(uuid.uuid4()) + ":"
script = pathlib.Path(__file__).with_name("money_ledger.lua").read_text()
tiers = {"L1": {"quota": 500000000, "models": []}, "L2": {"quota": 1500000000, "models": []}}
anchor = dt.datetime(2026, 1, 5, tzinfo=dt.timezone(dt.timedelta(hours=8)))
now = dt.datetime.now(anchor.tzinfo)
start = anchor + dt.timedelta(days=14 * ((now - anchor).days // 14))
end = start + dt.timedelta(days=14)
base = {"period": start.date().isoformat(), "period_start": start.isoformat(),
        "period_end": end.isoformat(), "period_start_unix": int(start.timestamp()),
        "period_end_unix": int(end.timestamp())}

def run(action, **overrides):
    request = dict(base, action=action, dimension="user", subject="alice", app="demo",
                   operator="test", occurred_at=now.isoformat())
    request.update(overrides)
    if "ticket_id" in request:
        request["fingerprint"] = json.dumps({k: v for k, v in request.items()
                                              if k not in ("occurred_at", "fingerprint")}, sort_keys=True)
    return json.loads(command("EVAL", script, 1, prefix, json.dumps(request), json.dumps(tiers), "{}"))

try:
    initial = run("meta")
    assert initial["period_days"] == 14
    assert run("cycle", period="stale-period", days=7, ticket_id="stale-before-config")["status"] == 409
    run("assign", tier="L1", ticket_id="assign")
    changed = run("cycle", days=7, ticket_id="set-cycle")
    assert changed["ok"] and run("meta")["period"] == initial["period"]
    assert run("meta")["next_period_days"] == 7
    old = run("query")
    assert old["limit"] == 500000000
    reset = run("reset", ticket_id="reset")
    new = run("meta")
    assert reset["ok"] and new["period"] != initial["period"]
    assert new["period_days"] == 7 and run("query")["limit"] == 500000000
    assert run("reset", ticket_id="reset") == reset
    assert run("reset", ticket_id="stale")["status"] == 409
    assert any(item["action"] == "reset" for item in run("audit", offset=0, limit=10)["records"])
    today = dt.datetime.now(anchor.tzinfo).date()
    custom_start = dt.datetime.combine(today - dt.timedelta(days=1), dt.time(), anchor.tzinfo)
    custom_end = custom_start + dt.timedelta(days=5)
    invalid = run("reset", period=new["period"], ticket_id="invalid-range", start_unix=int(custom_end.timestamp()),
                  end_unix=int((custom_end + dt.timedelta(days=1)).timestamp()))
    assert invalid["status"] == 400, invalid
    assert run("reset", period=new["period"], ticket_id="missing-end", start_unix=int(custom_start.timestamp()))["status"] == 400
    selected = run("reset", period=new["period"], ticket_id="selected-range", start_unix=int(custom_start.timestamp()),
                   end_unix=int(custom_end.timestamp()))
    selected_meta = run("meta")
    assert selected["ok"] and selected_meta["period_start"] == int(custom_start.timestamp())
    assert selected_meta["period_end"] == int(custom_end.timestamp())
    assert selected_meta["period_days"] == 5 and selected_meta["next_period_days"] == 7
    run("settle", period=initial["period"], payer_dimension="user", value=5,
        input_tokens=3, output_tokens=2, cost_micros=100, ticket_id="late-settle")
    assert run("query")["spent"] == 0
    state = json.loads(command("GET", prefix + "cycle_config"))
    state.update(anchor=int(dt.datetime.now().timestamp()) - 2 * 86400,
                 end_unix=int(dt.datetime.now().timestamp()) - 86400,
                 current_days=1, next_days=7, current_period="expired")
    command("SET", prefix + "cycle_config", json.dumps(state))
    rolled = run("meta")
    assert rolled["period"] != "expired" and rolled["period_days"] == 7
    print("cycle integration: PASS")
finally:
    keys = command("KEYS", prefix + "*")
    if keys:
        command("DEL", *keys)
