"""One module per iCloud surface. Importing a module is what registers its tools.

`server.py` imports all five and then runs; nothing here runs on its own. `markdown.py` is the
exception and registers nothing: it is a dependency-free helper `notes.py` calls, kept apart so it
can be exercised without Playwright or an Apple session.
"""
