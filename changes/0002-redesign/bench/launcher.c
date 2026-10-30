// Tiny static launcher: fork+exec argv[1..], wait4, print "maxrss_kb wall_ms exitcode" to fd 3. Avoids the
// parent-RSS-inherited-by-child artifact of ru_maxrss when the parent is a large process (python).
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>
#include <time.h>
#include <fcntl.h>
#include <sys/wait.h>
#include <sys/resource.h>
int main(int argc, char **argv) {
  struct timespec a, b; int st; struct rusage ru;
  clock_gettime(CLOCK_MONOTONIC, &a);
  pid_t p = fork();
  if (p == 0) { execv(argv[1], argv + 1); _exit(127); }
  wait4(p, &st, 0, &ru);
  clock_gettime(CLOCK_MONOTONIC, &b);
  double ms = (b.tv_sec - a.tv_sec) * 1e3 + (b.tv_nsec - a.tv_nsec) / 1e6;
  dprintf(3, "%ld %.3f %d\n", ru.ru_maxrss, ms, WIFEXITED(st) ? WEXITSTATUS(st) : 128 + WTERMSIG(st));
  return 0;
}
