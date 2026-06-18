#include <stdio.h>
#include <string.h>
#include <time.h>
#include <windows.h>

#define PATH_CAP 4096

typedef struct BackupStats {
    int files_copied;
    int directories_created;
    int items_skipped;
} BackupStats;

static void print_usage(const char *program_name) {
    fprintf(
        stderr,
        "Usage: %s [--source <blog_dir>] [--output <backup_dir>]\n"
        "\n"
        "Backs up blog data files and uploads into a timestamped directory.\n"
        "\n"
        "Options:\n"
        "  --source <blog_dir>   Blog project root. Default: current directory.\n"
        "  --output <backup_dir> Directory that will contain backup-* folders.\n"
        "                        Default: <blog_dir>\\\\backups\n"
        "  --help                Show this help message.\n",
        program_name
    );
}

static void normalize_separators(char *path) {
    while (*path != '\0') {
        if (*path == '/') {
            *path = '\\';
        }
        path++;
    }
}

static int build_full_path(const char *input, char *output, size_t output_size) {
    DWORD length = 0;

    if (input == NULL || output == NULL || output_size == 0) {
        return 0;
    }

    length = GetFullPathNameA(input, (DWORD)output_size, output, NULL);
    if (length == 0 || length >= output_size) {
        fprintf(stderr, "Failed to resolve path: %s\n", input);
        return 0;
    }

    normalize_separators(output);
    return 1;
}

static int path_exists(const char *path) {
    DWORD attributes = GetFileAttributesA(path);
    return attributes != INVALID_FILE_ATTRIBUTES;
}

static int path_is_directory(const char *path) {
    DWORD attributes = GetFileAttributesA(path);
    if (attributes == INVALID_FILE_ATTRIBUTES) {
        return 0;
    }
    return (attributes & FILE_ATTRIBUTE_DIRECTORY) != 0;
}

static int join_path(char *output, size_t output_size, const char *left, const char *right) {
    size_t left_length = 0;
    int needs_separator = 0;

    if (output == NULL || left == NULL || right == NULL) {
        return 0;
    }

    left_length = strlen(left);
    needs_separator = left_length > 0 && left[left_length - 1] != '\\' && left[left_length - 1] != '/';

    if (snprintf(output, output_size, "%s%s%s", left, needs_separator ? "\\" : "", right) >= (int)output_size) {
        fprintf(stderr, "Path too long while joining '%s' and '%s'\n", left, right);
        return 0;
    }

    normalize_separators(output);
    return 1;
}

static int ensure_directory(const char *path, BackupStats *stats) {
    char buffer[PATH_CAP];
    size_t length = 0;
    size_t i = 0;
    size_t start_index = 0;

    if (path == NULL || *path == '\0') {
        return 0;
    }

    if (snprintf(buffer, sizeof(buffer), "%s", path) >= (int)sizeof(buffer)) {
        fprintf(stderr, "Directory path too long: %s\n", path);
        return 0;
    }

    normalize_separators(buffer);
    length = strlen(buffer);

    if (length == 0) {
        return 0;
    }

    if (length >= 3 && buffer[1] == ':' && (buffer[2] == '\\' || buffer[2] == '/')) {
        start_index = 3;
    }

    for (i = start_index; i < length; i++) {
        if (buffer[i] == '\\') {
            char saved = buffer[i];

            buffer[i] = '\0';
            if (strlen(buffer) > 0) {
                if (!CreateDirectoryA(buffer, NULL)) {
                    DWORD error = GetLastError();
                    if (error != ERROR_ALREADY_EXISTS) {
                        fprintf(stderr, "Failed to create directory: %s (error %lu)\n", buffer, error);
                        return 0;
                    }
                } else if (stats != NULL) {
                    stats->directories_created++;
                }
            }
            buffer[i] = saved;
        }
    }

    if (!CreateDirectoryA(buffer, NULL)) {
        DWORD error = GetLastError();
        if (error != ERROR_ALREADY_EXISTS) {
            fprintf(stderr, "Failed to create directory: %s (error %lu)\n", buffer, error);
            return 0;
        }
    } else if (stats != NULL) {
        stats->directories_created++;
    }

    return 1;
}

static int ensure_parent_directory(const char *path, BackupStats *stats) {
    char buffer[PATH_CAP];
    char *last_separator = NULL;

    if (path == NULL) {
        return 0;
    }

    if (snprintf(buffer, sizeof(buffer), "%s", path) >= (int)sizeof(buffer)) {
        fprintf(stderr, "Path too long: %s\n", path);
        return 0;
    }

    normalize_separators(buffer);
    last_separator = strrchr(buffer, '\\');
    if (last_separator == NULL) {
        return 1;
    }

    *last_separator = '\0';
    if (*buffer == '\0') {
        return 1;
    }

    return ensure_directory(buffer, stats);
}

static int copy_file_to(const char *source, const char *destination, BackupStats *stats) {
    if (!ensure_parent_directory(destination, stats)) {
        return 0;
    }

    if (!CopyFileA(source, destination, FALSE)) {
        fprintf(stderr, "Failed to copy file: %s -> %s (error %lu)\n", source, destination, GetLastError());
        return 0;
    }

    if (stats != NULL) {
        stats->files_copied++;
    }

    return 1;
}

static int copy_directory_recursive(const char *source, const char *destination, BackupStats *stats) {
    char pattern[PATH_CAP];
    char source_child[PATH_CAP];
    char destination_child[PATH_CAP];
    WIN32_FIND_DATAA find_data;
    HANDLE handle = INVALID_HANDLE_VALUE;

    if (!ensure_directory(destination, stats)) {
        return 0;
    }

    if (!join_path(pattern, sizeof(pattern), source, "*")) {
        return 0;
    }

    handle = FindFirstFileA(pattern, &find_data);
    if (handle == INVALID_HANDLE_VALUE) {
        fprintf(stderr, "Failed to enumerate directory: %s (error %lu)\n", source, GetLastError());
        return 0;
    }

    do {
        if (strcmp(find_data.cFileName, ".") == 0 || strcmp(find_data.cFileName, "..") == 0) {
            continue;
        }

        if (!join_path(source_child, sizeof(source_child), source, find_data.cFileName)) {
            FindClose(handle);
            return 0;
        }

        if (!join_path(destination_child, sizeof(destination_child), destination, find_data.cFileName)) {
            FindClose(handle);
            return 0;
        }

        if ((find_data.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY) != 0) {
            if (!copy_directory_recursive(source_child, destination_child, stats)) {
                FindClose(handle);
                return 0;
            }
        } else {
            if (!copy_file_to(source_child, destination_child, stats)) {
                FindClose(handle);
                return 0;
            }
        }
    } while (FindNextFileA(handle, &find_data) != 0);

    if (GetLastError() != ERROR_NO_MORE_FILES) {
        fprintf(stderr, "Directory iteration failed: %s (error %lu)\n", source, GetLastError());
        FindClose(handle);
        return 0;
    }

    FindClose(handle);
    return 1;
}

static int copy_relative_file(
    const char *source_root,
    const char *backup_root,
    const char *relative_path,
    BackupStats *stats
) {
    char source_path[PATH_CAP];
    char destination_path[PATH_CAP];

    if (!join_path(source_path, sizeof(source_path), source_root, relative_path)) {
        return 0;
    }

    if (!path_exists(source_path)) {
        fprintf(stdout, "Skip missing file: %s\n", source_path);
        if (stats != NULL) {
            stats->items_skipped++;
        }
        return 1;
    }

    if (!join_path(destination_path, sizeof(destination_path), backup_root, relative_path)) {
        return 0;
    }

    fprintf(stdout, "Copy file: %s\n", relative_path);
    return copy_file_to(source_path, destination_path, stats);
}

static int copy_relative_directory(
    const char *source_root,
    const char *backup_root,
    const char *relative_path,
    BackupStats *stats
) {
    char source_path[PATH_CAP];
    char destination_path[PATH_CAP];

    if (!join_path(source_path, sizeof(source_path), source_root, relative_path)) {
        return 0;
    }

    if (!path_is_directory(source_path)) {
        fprintf(stdout, "Skip missing directory: %s\n", source_path);
        if (stats != NULL) {
            stats->items_skipped++;
        }
        return 1;
    }

    if (!join_path(destination_path, sizeof(destination_path), backup_root, relative_path)) {
        return 0;
    }

    fprintf(stdout, "Copy directory: %s\n", relative_path);
    return copy_directory_recursive(source_path, destination_path, stats);
}

static int build_default_output_root(char *output, size_t output_size, const char *source_root) {
    return join_path(output, output_size, source_root, "backups");
}

static int build_timestamp(char *output, size_t output_size) {
    time_t now = time(NULL);
    struct tm local_now;

    if (output == NULL || output_size < 32) {
        return 0;
    }

    if (localtime_s(&local_now, &now) != 0) {
        fprintf(stderr, "Failed to read local time.\n");
        return 0;
    }

    if (strftime(output, output_size, "backup-%Y%m%d-%H%M%S", &local_now) == 0) {
        fprintf(stderr, "Failed to format timestamp.\n");
        return 0;
    }

    return 1;
}

int main(int argc, char **argv) {
    char source_root[PATH_CAP] = ".";
    char output_root[PATH_CAP] = "";
    char resolved_source_root[PATH_CAP];
    char backup_name[64];
    char backup_root[PATH_CAP];
    BackupStats stats = {0, 0, 0};
    int i = 0;

    for (i = 1; i < argc; i++) {
        if (strcmp(argv[i], "--help") == 0) {
            print_usage(argv[0]);
            return 0;
        }

        if (strcmp(argv[i], "--source") == 0) {
            if (i + 1 >= argc) {
                fprintf(stderr, "--source requires a value.\n");
                print_usage(argv[0]);
                return 1;
            }
            if (!build_full_path(argv[++i], source_root, sizeof(source_root))) {
                return 1;
            }
            continue;
        }

        if (strcmp(argv[i], "--output") == 0) {
            if (i + 1 >= argc) {
                fprintf(stderr, "--output requires a value.\n");
                print_usage(argv[0]);
                return 1;
            }
            if (!build_full_path(argv[++i], output_root, sizeof(output_root))) {
                return 1;
            }
            continue;
        }

        fprintf(stderr, "Unknown argument: %s\n", argv[i]);
        print_usage(argv[0]);
        return 1;
    }

    if (!build_full_path(source_root, resolved_source_root, sizeof(resolved_source_root))) {
        return 1;
    }

    if (snprintf(source_root, sizeof(source_root), "%s", resolved_source_root) >= (int)sizeof(source_root)) {
        fprintf(stderr, "Resolved source path is too long.\n");
        return 1;
    }

    if (output_root[0] == '\0') {
        if (!build_default_output_root(output_root, sizeof(output_root), source_root)) {
            return 1;
        }
    }

    if (!build_timestamp(backup_name, sizeof(backup_name))) {
        return 1;
    }

    if (!join_path(backup_root, sizeof(backup_root), output_root, backup_name)) {
        return 1;
    }

    fprintf(stdout, "Source root : %s\n", source_root);
    fprintf(stdout, "Backup root : %s\n", backup_root);

    if (!ensure_directory(backup_root, &stats)) {
        return 1;
    }

    if (!copy_relative_file(source_root, backup_root, "data\\blog.db", &stats)) {
        return 1;
    }

    if (!copy_relative_file(source_root, backup_root, "data\\blog.db-wal", &stats)) {
        return 1;
    }

    if (!copy_relative_file(source_root, backup_root, "data\\blog.db-shm", &stats)) {
        return 1;
    }

    if (!copy_relative_directory(source_root, backup_root, "web\\static\\uploads", &stats)) {
        return 1;
    }

    fprintf(
        stdout,
        "\nBackup completed. Files copied: %d, directories created: %d, items skipped: %d\n",
        stats.files_copied,
        stats.directories_created,
        stats.items_skipped
    );

    return 0;
}
