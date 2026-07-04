# spawn_procs: shell-out fan-out. Run N tiny subprocesses serially,
# capture each stdout, parse the echoed integer, sum them.
# OS-process-spawn-bound: measures orchestration/launch overhead, not VM speed.
import subprocess

n = 40
total = 0
for i in range(n):
    out = subprocess.run(
        ["cmd", "/c", "echo", str(i)],
        capture_output=True, text=True,
    ).stdout
    total += int(out.strip())
print(total)
