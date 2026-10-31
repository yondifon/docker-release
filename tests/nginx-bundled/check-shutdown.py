import pathlib
import runpy

runpy.run_path(str(pathlib.Path(__file__).resolve().parents[1] / "bundled/check-shutdown.py"), run_name="__main__")
