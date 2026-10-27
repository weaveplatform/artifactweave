"""Pinned huggingface_hub 2.3.0 acceptance client for current ML layout."""
import os
import pathlib
import sys
import urllib.request
from huggingface_hub import HfApi, snapshot_download

endpoint = os.environ["HF_ENDPOINT"]
token = os.environ["HF_TOKEN"]
mode = sys.argv[1]
root = pathlib.Path(sys.argv[2])
root.mkdir(parents=True, exist_ok=True)
api = HfApi(endpoint=endpoint, token=token)
for kind in ("model", "dataset"):
    if mode == "publish":
        source = root / (kind + "-source")
        source.mkdir()
        (source / "config.json").write_text('{"kind":"' + kind + '"}')
        (source / "weights.bin").write_bytes(b"X" * (1024 * 1024 + 1))
        commit = api.upload_folder(repo_id="acme/demo", repo_type=kind, folder_path=source)
        assert len(commit.oid) == 40, commit
    destination = root / (kind + "-download")
    path = pathlib.Path(snapshot_download(repo_id="acme/demo", repo_type=kind,
        endpoint=endpoint, token=token, local_dir=destination, max_workers=1))
    assert (path / "config.json").read_text() == '{"kind":"' + kind + '"}'
    assert (path / "weights.bin").stat().st_size == 1024 * 1024 + 1
    tree = list(api.list_repo_tree(repo_id="acme/demo", repo_type=kind,
        recursive=True, token=token))
    assert {entry.path for entry in tree} == {"config.json", "weights.bin"}
    prefix = "datasets/" if kind == "dataset" else ""
    request = urllib.request.Request(
        endpoint + "/" + prefix + "acme/demo/resolve/main/weights.bin",
        headers={"Authorization": "Bearer " + token, "Range": "bytes=100-199"})
    with urllib.request.urlopen(request) as partial:
        assert partial.status == 206, partial.status
        assert partial.read() == b"X" * 100
if mode == "publish":
    named = api.upload_folder(repo_id="acme/demo", repo_type="model",
        revision="v1", folder_path=root / "model-source")
    assert len(named.oid) == 40
    tagged = pathlib.Path(snapshot_download(repo_id="acme/demo", repo_type="model",
        revision="v1", endpoint=endpoint, token=token,
        local_dir=root / "named-download", max_workers=1))
    assert (tagged / "config.json").read_text() == '{"kind":"model"}'
print("Hugging Face model and dataset " + mode + "/download passed")
