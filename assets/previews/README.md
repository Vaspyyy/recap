# Re-recording the README preview

The preview is recorded with [VHS](https://github.com/charmbracelet/vhs) in an anonymous demo repository.

```sh
python3 make_demo.py                       # builds /var/tmp/recap-demo (a small Go repository with an uncommitted refactor)
cd /var/tmp/recap-demo && mkdir -p tapes && cp <this folder>/*.tape tapes/ && cd tapes
vhs hero.tape
cp hero.gif <repo>/assets/
rm -rf /var/tmp/recap-demo
```

Needs `vhs` (with `ttyd` and `ffmpeg`), git and a JetBrainsMono Nerd Font. `common.tape` sets a fake `HOME`,
so no real user or path ends up in the image.
