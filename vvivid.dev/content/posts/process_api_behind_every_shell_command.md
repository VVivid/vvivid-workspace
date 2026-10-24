---
title: "The Process API Behind Every Shell Command"
date: 2026-06-15T13:31:48+07:00
author: "Ou David"
draft: false
tags: ["operating-systems", "unix", "processes", "systems-programming"]
categories: ["Technical Deep Dives"]
description: "What OSTEP's Process API chapter taught me about fork, exec, wait, and the shell."
image: "images/thumbnails/process-api-shell.png"
toc: false
math: false
---

I've been reading [Operating Systems: Three Easy Pieces](https://pages.cs.wisc.edu/~remzi/OSTEP/), and today I went through chapter 05, the interlude on the Process API. It is a short chapter, but it clarified something important about how processes actually work.

If you want to see the same idea in practical Go code, I put a small runnable example in the public case-study repo: [go/process-api-shell](https://gitlab.vvivid.dev/showcase/go-lang-case-study/-/tree/main/go/process-api-shell). It uses Go's standard `os/exec` package to run commands, redirect output, build a simple pipeline, and wait for the processes to finish.

```sh
grep "error" app.log | wc -l
```

This is a simple shell command. One program reads a file, another program counts the matching lines, and the shell connects them together. Underneath that simple line is one of the most important UNIX ideas: process creation, process replacement, and process coordination.

The chapter focuses on three calls:

- `fork()`
- `exec()`
- `wait()`

You can do a lot with just these three system calls. Once I thought about them through the shell, the design started to feel very intentional.

### The Real Problem

The operating system has to answer a practical question:

> How should one program create and control another program?

We can come up with something very simple like:

```c
run_program("wc", args);
```

That would be easy to understand. But we would lose a lot of useful control. For example, the parent might want to change where its input comes from, where its output goes, which environment variables it sees, or whether the parent should wait for its child.

UNIX solves this by separating the process lifecycle into smaller steps.

### Creates a Copy with `fork()`

When a process calls `fork()`, the operating system creates a new process that is almost a copy of the current one. The child gets its own process ID, its own address space, and copies of the parent's variables and file descriptors. Both processes continue running from the same place in the code, but `fork()` returns different values in each one:

```c
pid_t pid = fork();

if (pid < 0) {
    // fork failed
} else if (pid == 0) {
    // child process
} else {
    // parent process, pid is the child's process ID
}
```

This is the part that confused me at first. I thought the child process would be created first, then wait until the parent explicitly told it to start. But that is not how `fork()` works. Once the fork succeeds, the child is runnable. The CPU scheduler still decides when it actually runs, but from the program's point of view there are now two processes continuing from the same line.

Because the file descriptors are copied, both processes may still write to the same terminal or file unless we change that. The return value is what lets the program split its behavior: the child can do child-specific work, while the parent keeps control of the larger flow.

The important detail is that the child is still a separate process. If the child changes a variable in memory, it is not changing the parent's copy of that variable.

### Makes Ordering Explicit with `wait()`

Once there are two processes, scheduling becomes visible.

After `fork()`, either the parent or child may run first. That is the first nondeterministic part. We should not build logic that depends on whichever process happens to run first. If the parent needs the child to finish before continuing, it has to say that explicitly:

```c
pid_t pid = fork();

if (pid == 0) {
    printf("child\n");
} else {
    wait(NULL);
    printf("parent\n");
}
```

Here, the parent blocks until the child exits. That gives the program a real ordering guarantee. Without `wait()`, the output order is up to the scheduler.

This is a useful system design lesson beyond operating systems. Whenever we work with concurrency, we have to know which parts of the program are nondeterministic and where the real synchronization points are. If we need a sequence, we should create one explicitly.

### Replaces the Current Program with `exec()`

`fork()` by itself only gives us another copy of the same program. That is useful sometimes, but a shell usually wants to run a different program.

That is where `exec()` comes in.

In the child process, we can call one of the `exec` functions to replace the current process image with another program:

```c
char *args[] = {"wc", "-l", "app.log", NULL};
execvp(args[0], args);

// If execvp succeeds, this line never runs.
perror("execvp");
```

The key idea is that `exec()` does not create a new process. It transforms the current process into a different program. The process keeps its PID, but its code, stack, heap, and program image are replaced.

That is why the common shell pattern is:

1. The shell calls `fork()`.
2. The child prepares its environment.
3. The child calls `exec()` to become the requested program.
4. The parent calls `wait()` if it is running the command in the foreground.

Once you understand this flow, a shell starts to feel like a loop around these primitives.

### Why Split `fork()` and `exec()`?

The design becomes powerful because there is a gap between `fork()` and `exec()`.

In that gap, the child is still running shell-controlled code. It has not become `grep`, `wc`, `ls`, or any other program yet. The shell can change the child process before the new program starts.

For example, output redirection works because the child can change its file descriptors before calling `exec()`:

```c
pid_t pid = fork();

if (pid == 0) {
    close(STDOUT_FILENO);
    open("out.txt", O_CREAT | O_WRONLY | O_TRUNC, 0644);

    char *args[] = {"wc", "-l", "app.log", NULL};
    execvp(args[0], args);
    perror("execvp");
}

wait(NULL);
```

The future `wc` program does not need to know anything about shell redirection. It still writes to standard output. The trick is that standard output now points to a file before `wc` starts running.

That is a clean abstraction. The shell handles process setup. The program just reads from standard input and writes to standard output.

### Pipes Use the Same Idea

Pipes are another version of the same pattern.

When we run:

```sh
grep "error" app.log | wc -l
```

the shell creates a pipe, forks child processes, connects one child's standard output to the pipe's write end, connects the other child's standard input to the pipe's read end, and then calls `exec()` in both children.

Neither `grep` nor `wc` needs custom logic for this composition. `grep` writes bytes. `wc` reads bytes. The shell and operating system make the connection.

This is the part of the chapter I liked most. The API is not only about starting a process. It is about making small programs composable without requiring those programs to know about the other programs around them. That is separation of concerns.

### The Takeaway

The UNIX process API looks odd at first because `fork()` is not how we usually expect function calls to behave. But the split between `fork()`, `exec()`, and `wait()` gives the shell a precise place to control a child process before it becomes another program.

That small design decision explains a lot of daily command-line behavior:

- running a program,
- waiting for it to finish,
- redirecting output,
- connecting programs with pipes,
- keeping programs simple by standardizing input and output.

The shell feels high level, but it is built from low-level process primitives. That is the beauty of the design: each primitive is small, but together they create a flexible programming environment.
