"""Build the pinned upstream geosite compiler; never run on prebuilt installs."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys


PIN = dict(
    module='github.com/v2fly/domain-list-community',
    version='v0.0.0-20251220113846-6f3acc3ba952',
    commit='6f3acc3ba95299031cf408232e2e65e2c892fd2d',
    sum='h1:L9kXr0RzdMeOt2Z+t4WVg+FYYN2/OmhJlX9PMfWKFxw=',
    goModSum='h1:VDHN8NYyJZ9JT6ssU3Sv9x0RdBrrFeWcjRwXbGLUY/Y=',
)


def build(output, license_path, env=None):
    env = dict(os.environ if env is None else env, GOWORK='off')
    # Use an empty working directory so the helper module is not changed.
    output = Path(output).resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    downloaded = json.loads(subprocess.check_output(
        ['go', 'mod', 'download', '-json', PIN['module'] + '@' + PIN['version']],
        cwd=output.parent, env=env, text=True))
    if any(downloaded.get(key) != PIN[pin] for key, pin in (
            ('Path', 'module'), ('Version', 'version'), ('Sum', 'sum'), ('GoModSum', 'goModSum'))):
        raise ValueError('domain-list-community source checksum mismatch')
    source = Path(downloaded['Dir'])
    subprocess.run(['go', '-C', str(source), 'build', '-mod=readonly', '-buildvcs=false',
                    '-trimpath', '-ldflags', '-s -w', '-o', str(output), '.'],
                   env=env, check=True)
    license_path = Path(license_path)
    license_path.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source / 'LICENSE', license_path)


if __name__ == '__main__':
    if len(sys.argv) != 3:
        sys.exit('usage: dlc_builder.py OUTPUT_BINARY OUTPUT_LICENSE')
    build(sys.argv[1], sys.argv[2])
