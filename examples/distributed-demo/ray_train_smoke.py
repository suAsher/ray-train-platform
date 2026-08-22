#!/usr/bin/env python3
"""Same DDP proof as ddp_smoke, intended for a Ray-managed multi-node run."""

from ddp_smoke import main


if __name__ == "__main__":
    main()
