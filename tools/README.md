# C Automation Tool

`blog_backup.c` is a standalone Windows C program that creates a timestamped backup for:

- `data/blog.db`
- `data/blog.db-wal`
- `data/blog.db-shm`
- `web/static/uploads`

Example build and run:

```powershell
gcc .\tools\blog_backup.c -o .\tools\blog_backup.exe
.\tools\blog_backup.exe --source . --output .\backups
```
